package machine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/timestamppb"

	identityv1 "github.com/samir2015dzh/aide/gen/go/aide/identity/v1"
	machinev1 "github.com/samir2015dzh/aide/gen/go/aide/machine/v1"
	sessionv1 "github.com/samir2015dzh/aide/gen/go/aide/session/v1"
	"github.com/samir2015dzh/aide/internal/localipc"
	"github.com/samir2015dzh/aide/internal/paths"
	"github.com/samir2015dzh/aide/internal/provider"
	"github.com/samir2015dzh/aide/internal/resident"
	"github.com/samir2015dzh/aide/pkg/identity"
)

// The control socket lets `aided project …`, `aided roots …` and `aided config …` change a
// running machine's projects and workspace roots, `aided doctor` and
// `status` ask it how it is, and `uninstall` and `erase` whether data of
// Her's, from before Aide dropped her, is kept (the machine holds its data directory). Projects are the machine's own settings: whoever has a
// shell as this user can do as much already, so only this user may connect.

func (n *Machine) serveControl(ctx context.Context) {
	path := paths.Control(n.cfg.DataDir)
	ln, err := localipc.Listen(path)
	if err != nil {
		n.log.Warn("control socket", "err", err)
		return
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /settings", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		cmd := &identityv1.AdminCommand{}
		if err := protojson.Unmarshal(body, cmd); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		msg, err := n.ApplyLocal(r.Context(), cmd)
		if err != nil {
			http.Error(w, err.Error(), http.StatusUnprocessableEntity)
			return
		}
		io.WriteString(w, msg)
	})
	mux.HandleFunc("GET /projects", func(w http.ResponseWriter, r *http.Request) {
		res, err := (&machineService{n: n}).projects(r.Context(), &Principal{Kind: PrincipalOwner}, false)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		b, _ := protojson.Marshal(res)
		w.Write(b)
	})
	mux.HandleFunc("GET /config", func(w http.ResponseWriter, r *http.Request) {
		c, err := n.ExportConfig()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		json.NewEncoder(w).Encode(c)
	})
	mux.HandleFunc("POST /config", func(w http.ResponseWriter, r *http.Request) {
		var c ConfigFile
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&c); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		json.NewEncoder(w).Encode(n.ApplyConfig(r.Context(), c, r.URL.Query().Get("dry_run") == "1"))
	})
	mux.HandleFunc("GET /roots", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(n.Roots())
	})
	mux.HandleFunc("GET /check", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(n.Check(r.Context()))
	})
	// What power saving does now (docs/resident.md §2.3), for scripts: cheaper than /check.
	mux.HandleFunc("GET /power", func(w http.ResponseWriter, r *http.Request) {
		st, _ := resident.Current()
		json.NewEncoder(w).Encode(st)
	})
	mux.HandleFunc("GET /her-data", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(n.herDataKept())
	})
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		srv.Close()
		os.Remove(path)
	}()
	srv.Serve(ln)
}

// Roots are the machine's workspace roots, for `aided roots`.
type Roots struct {
	Roots   []string // none: any folder
	Outside []string // projects outside them, "id (path)"
}

func (n *Machine) Roots() Roots {
	return Roots{Roots: n.WorkspaceRoots(), Outside: n.ProjectsOutsideRoots()}
}

// Check is how this machine is, for `aided doctor`, `status` and
// `update`; its coding agents are looked at now.
type Check struct {
	MachineID, Name, OwnerID, RelayURL, Version string
	Paired, Connected                           bool
	Agents                                      map[string]provider.Info
	Busy                                        int                  // sessions a restart would stop
	Her                                         *machinev1.HerStatus // that this machine has none, and whether her data is kept
	Power                                       *resident.State      `json:",omitempty"` // the running aided's power saving
}

func (n *Machine) Check(ctx context.Context) Check {
	return Check{MachineID: n.MachineID(), Name: n.Name(), OwnerID: n.OwnerID(), RelayURL: n.RelayURL(), Version: Version,
		Paired: n.Paired(), Connected: n.Connected(), Agents: n.Discover(ctx), Busy: n.Busy(), Her: n.herStatus(), Power: power()}
}

// Busy counts the sessions a restart would stop: queued, preparing,
// running or waiting for an answer.
func (n *Machine) Busy() int {
	return n.sessions.Running() + int(n.sessions.counts[sessionv1.SessionStatus_SESSION_STATUS_QUEUED].Load())
}

// power is this process's power saving, when it runs one (aided run).
func power() *resident.State {
	if st, ok := resident.Current(); ok {
		return &st
	}
	return nil
}

// ApplyLocal applies a change from this machine's own user: adding or
// removing a project, or setting the workspace roots; nothing else.
func (n *Machine) ApplyLocal(ctx context.Context, cmd *identityv1.AdminCommand) (string, error) {
	switch cmd.Command.(type) {
	case *identityv1.AdminCommand_AddProject, *identityv1.AdminCommand_RemoveProject, *identityv1.AdminCommand_SetWorkspaceRoots:
	default:
		return "", errors.New("only projects and workspace roots can be changed on the machine itself")
	}
	cmd.OwnerId, cmd.TargetMachineId, cmd.CommandId = n.OwnerID(), n.machineID, identity.RandomCode(16)
	cmd.ExpiresAt = timestamppb.New(time.Now().Add(time.Minute))
	return n.applyAdmin(ctx, cmd, "this machine's user")
}

// ControlRequest sends a running machine's control socket a request; ok is
// false when no machine listens there.
func ControlRequest(ctx context.Context, dataDir, method, path string, body []byte) (out []byte, ok bool, err error) {
	sock := paths.Control(dataDir)
	if _, err := os.Stat(sock); err != nil {
		return nil, false, nil
	}
	hc := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return localipc.Dial(ctx, sock)
	}}, Timeout: 30 * time.Second}
	req, _ := http.NewRequestWithContext(ctx, method, "http://machine"+path, bytes.NewReader(body))
	res, err := hc.Do(req)
	if err != nil {
		return nil, false, nil // a stale socket: nobody listens
	}
	defer res.Body.Close()
	out, _ = io.ReadAll(res.Body)
	if res.StatusCode != http.StatusOK {
		return nil, true, errors.New(strings.TrimSpace(string(out)))
	}
	return out, true, nil
}
