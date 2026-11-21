package machine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	"connectrpc.com/connect"

	machinev1 "github.com/samir2015dzh/aide/gen/go/aide/machine/v1"
	"github.com/samir2015dzh/aide/internal/shellenv"
	"github.com/samir2015dzh/aide/pkg/identity"
)

// Her is no longer part of Aide. What is left here is for the machines that
// ran her once: saying so to the apps that still ask, and what an owner who
// uninstalls or erases everything expects to go with the rest (her home,
// her keys). Nothing here starts her or talks to her.

const (
	// kHer was the owner's setting ("1" on, "0" off): any value says Her was
	// on here once. kHerGrant and kHerNews were hers on the mesh.
	kHer      = "her"
	kHerGrant = "her_grant"
	kHerNews  = "her_news"
	// herMark is the file her left in a home she made.
	herMark = ".her-home"
	// herRemoved is HerStatus.unsupported on every machine now: an app from
	// before reads it as a machine that can't run Her, not as one to update.
	herRemoved = "removed"
)

// setHer's answers, which the owner's device reads: HerErased once her data
// is erased, HerOff while it stays; one that couldn't erase it says why
// after HerOff.
const (
	HerOff    = "Her is off"
	HerErased = "Her is off, and her data is erased"
)

var errOwnerOnly = connect.NewError(connect.CodePermissionDenied, errors.New("owner only"))

// herStatus tells the apps from before that this machine has no Her, and
// whether her data from then is still here.
func (n *Machine) herStatus() *machinev1.HerStatus {
	n.mu.Lock()
	defer n.mu.Unlock()
	return &machinev1.HerStatus{Unsupported: herRemoved, Data: n.herData}
}

// setHer answers AdminCommand.set_her from an app that still has the
// switch. On: refused, there is no Her to turn on. Off: nothing runs, so it
// is off already; with erase her data goes, as the app asked.
func (n *Machine) setHer(on, erase bool) (string, error) {
	if on {
		return "", connect.NewError(connect.CodeFailedPrecondition, errors.New("Aide no longer has Her: update the app on this device"))
	}
	if !erase {
		return HerOff, nil
	}
	if err := eraseHerHome(n); err != nil {
		return HerOff + ", but her data stays: " + err.Error(), nil
	}
	n.mu.Lock()
	n.herData = false
	n.mu.Unlock()
	n.kickStatus()
	return HerErased, nil
}

// SetHerGrant was how the owner's device gave this machine's Her her grant.
func (s *adminService) SetHerGrant(context.Context, *machinev1.SetHerGrantRequest) (*machinev1.SetHerGrantResponse, error) {
	return nil, connect.NewError(connect.CodeUnimplemented, errors.New("Aide no longer has Her"))
}

// forgetHer drops what a machine kept for Her as one of the owner's agents:
// her grant and the news that waited for her in the store, her client key
// and her inbox key on disk. The grant itself is the owner's to revoke (it
// is signed by their root key); without the keys nobody can use it.
func (n *Machine) forgetHer() {
	for _, k := range []string{kHerGrant, kHerNews} {
		if v, _ := n.st.Get(k); len(v) > 0 {
			n.st.Set(k, []byte{})
		}
	}
	for _, p := range []string{filepath.Join(n.cfg.DataDir, "keys", "her.key"), filepath.Join(n.cfg.DataDir, "keys", "her-inbox.key")} {
		if err := os.Remove(p); err == nil {
			n.log.Info("Her's key removed: Aide no longer has Her", "path", p)
		}
	}
}

// herHome is her home as her environment named it (HER_HOME, else ~/.her).
func herHome() string {
	dir, _ := os.UserHomeDir()
	for _, kv := range shellenv.Environ() {
		if v, ok := strings.CutPrefix(kv, "HER_HOME="); ok {
			if strings.TrimSpace(v) == "" {
				break
			}
			if !filepath.IsAbs(v) {
				v = filepath.Join(dir, v)
			}
			return v
		}
	}
	return filepath.Join(dir, ".her")
}

// herHomeExists reports whether her home is there.
func herHomeExists() bool {
	_, err := os.Stat(herHome())
	return err == nil
}

// herDataKept is HerData, on the control socket.
type herDataKept struct {
	Home string
	Kept bool
}

func (n *Machine) herDataKept() herDataKept {
	n.mu.Lock()
	defer n.mu.Unlock()
	return herDataKept{Home: herHome(), Kept: n.herData}
}

// HerData is where her data is on the machine in cfg, when Her was on there
// once and it is still there: for `aided uninstall` and `aided erase`
// to ask whether it goes too. A running machine (its service) is asked.
func HerData(cfg Config) (home string, ok bool, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if out, running, err := ControlRequest(ctx, cfg.DataDir, "GET", "/her-data", nil); running {
		var d herDataKept
		if err == nil {
			err = json.Unmarshal(out, &d)
		}
		return d.Home, d.Kept, err
	}
	n, err := Open(cfg)
	if err != nil {
		return "", false, err
	}
	defer n.Close()
	d := n.herDataKept()
	return d.Home, d.Kept, nil
}

// EraseHer erases her data for Erase Everything (docs/erase-everything.md),
// on a machine that ever had Her on: kept at its uninstall, it would
// outlive the owner. The machine must not be running.
func EraseHer(cfg Config) error {
	n, err := Open(cfg)
	if err != nil {
		return err
	}
	defer n.Close()
	if was, _ := n.st.Get(kHer); len(was) == 0 {
		return nil
	}
	return eraseHerHome(n)
}

// eraseHerHome deletes her home, and on a Mac her Keychain items unless
// HER_CREDENTIAL_STORE kept her off the Keychain. A home is deleted whole,
// so only one that is hers alone: ~/.her, or one her made; else nothing is
// erased.
func eraseHerHome(n *Machine) error {
	userHome, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	home := herHome()
	if !hersAlone(home, userHome) {
		return fmt.Errorf("her home %s was not made by her and may hold other files: not erased; delete her files there yourself", home)
	}
	if runtime.GOOS == "darwin" && !slices.ContainsFunc(shellenv.Environ(), func(kv string) bool { return strings.HasPrefix(kv, "HER_CREDENTIAL_STORE=") }) {
		if err := deleteHerKeychainItems("her"); err != nil {
			return err
		}
	}
	if err := os.RemoveAll(home); err != nil {
		return fmt.Errorf("erasing her home: %w", err)
	}
	n.log.Info("Her's data erased", "home", home)
	return nil
}

// hersAlone reports whether erasing may delete all of home: ~/.her, a home
// her made, or none at all.
func hersAlone(home, userHome string) bool {
	if filepath.Clean(home) == filepath.Join(userHome, ".her") {
		return true
	}
	if _, err := os.Lstat(home); errors.Is(err, os.ErrNotExist) {
		return true
	}
	_, err := os.Lstat(filepath.Join(home, herMark))
	return err == nil
}

// deleteHerKeychainItems deletes every generic password of service; a
// variable for tests, which must never touch the Keychain.
var deleteHerKeychainItems = func(service string) error {
	// One item per call; security fails once none is left (exit 44).
	for range 10000 {
		err := exec.Command("/usr/bin/security", "delete-generic-password", "-s", service).Run()
		var exit *exec.ExitError
		if errors.As(err, &exit) && exit.ExitCode() == 44 {
			return nil
		}
		if err != nil {
			return fmt.Errorf("erasing her Keychain items (%s): %w", service, err)
		}
	}
	return fmt.Errorf("erasing her Keychain items (%s): too many", service)
}

// statusFor is the machine's status as p may see it: the screen's carries
// the name of a device, and direct_addrs internal/public endpoints, for the
// owner's devices only.
func (n *Machine) statusFor(p *Principal) *machinev1.MachineStatus {
	st := n.Status()
	if p == nil || p.Kind != PrincipalOwner {
		st.Her, st.Screen, st.DirectAddrs = nil, nil, nil
		for _, a := range st.Agents {
			a.ModelCatalogs = slices.DeleteFunc(a.ModelCatalogs, func(c *machinev1.AgentModelCatalog) bool {
				if p == nil || p.Grant == nil || !identity.Allows(p.Grant.Agents, a.Name) {
					return true
				}
				if c.Scope == "isolated" {
					return false
				}
				_, id, ok := strings.Cut(c.Scope, "/")
				if !ok {
					return true
				}
				_, _, err := n.sessions.find(p, id, false)
				return err != nil
			})
		}
	}
	return st
}
