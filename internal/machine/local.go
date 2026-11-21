package machine

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	identityv1 "github.com/samir2015dzh/aide/gen/go/aide/identity/v1"
	machinev1 "github.com/samir2015dzh/aide/gen/go/aide/machine/v1"
	relayv1 "github.com/samir2015dzh/aide/gen/go/aide/relay/v1"
	sessionv1 "github.com/samir2015dzh/aide/gen/go/aide/session/v1"
	"github.com/samir2015dzh/aide/internal/workstats"
	"github.com/samir2015dzh/aide/pkg/identity"
	"github.com/samir2015dzh/aide/pkg/transport"
)

// Local is how the app on a Mac that is only a machine of its owner (not
// one of the owner's trusted devices) reaches this machine: in process, as
// the owner reaches it end to end, for its own projects and sessions. The
// app's local socket admits only this user, who can read the machine's
// files anyway; nothing here adds trust.
type Local struct {
	n *Machine
	p *Principal
}

func (n *Machine) Local() *Local { return &Local{n: n, p: &Principal{Kind: PrincipalOwner}} }

func (l *Local) MachineID() string { return l.n.machineID }

// Connected is whether the machine is connected to its relay: whether the
// owner's agents and other devices can reach it.
func (l *Local) Connected() bool { return l.n.Connected() }

// Info is the machine's state; the coding agents as the last check found
// them (the first check starts if none has run).
func (l *Local) Info() *machinev1.GetInfoResponse {
	l.n.CheckAgents(false, AgentsOnce)
	return &machinev1.GetInfoResponse{MachineId: l.n.machineID, MachineName: l.n.Name(), Status: l.n.Status()}
}

// CheckAgents checks the coding agents again, in the background.
func (l *Local) CheckAgents() { l.n.CheckAgents(true, 0) }

// TestAgent runs Test on this device's own machine.
func (l *Local) TestAgent(ctx context.Context, agent string) (*machinev1.TestAgentResponse, error) {
	return l.n.TestAgent(ctx, agent)
}

// CustomAgents, SaveCustomAgent, RemoveCustomAgent and TestCustomAgent are
// this device's own machine's custom agents (customagents.go).
func (l *Local) CustomAgents() *machinev1.ListCustomAgentsResponse { return l.n.CustomAgents() }

func (l *Local) SaveCustomAgent(a *machinev1.CustomAgent) error { return l.n.SaveCustomAgent(a) }

func (l *Local) RemoveCustomAgent(name string) error { return l.n.RemoveCustomAgent(name) }

func (l *Local) TestCustomAgent(ctx context.Context, a *machinev1.CustomAgent) (*machinev1.TestCustomAgentResponse, error) {
	return l.n.TestCustomAgent(ctx, a)
}

// FindRepos lists the repositories for Add a Project, within the workspace roots.
func (l *Local) FindRepos(ctx context.Context) []*machinev1.Repo {
	return FindRepos(ctx, l.n.WorkspaceRoots())
}

// ListFolders lists a folder for Add a Project, within the workspace roots.
func (l *Local) ListFolders(path string, hidden bool) (*machinev1.ListFoldersResponse, error) {
	return ListFolders(l.n.WorkspaceRoots(), path, hidden)
}

// ExecLog is the machine's ops log (ops.go), newest first.
func (l *Local) ExecLog() (*machinev1.ListExecLogResponse, error) { return l.n.ExecLog() }

// DSHSessions and ReadDSHSession are DeepSeek Harness's own sessions here
// (dshsessions.go).
func (l *Local) DSHSessions(ctx context.Context) (*machinev1.ListDSHSessionsResponse, error) {
	return l.n.DSHSessions(ctx)
}

// SetDSHTakeover, SendDSHMessage and InterruptDSHSession take over a DSH
// session here (dshtakeover.go), as the owner.
func (l *Local) SetDSHTakeover(req *machinev1.SetDSHTakeoverRequest) (*machinev1.SetDSHTakeoverResponse, error) {
	return l.n.SetDSHTakeover(req)
}

func (l *Local) SendDSHMessage(ctx context.Context, req *machinev1.SendDSHMessageRequest) (*machinev1.SendDSHMessageResponse, error) {
	return l.n.SendDSHMessage(ctx, req)
}

func (l *Local) DSHSessionOptions(ctx context.Context, id string) (*machinev1.DSHSessionOptionsResponse, error) {
	return l.n.DSHSessionOptions(ctx, id)
}

func (l *Local) PrepareDSHBridge() (*machinev1.PrepareDSHBridgeResponse, error) {
	return l.n.PrepareDSHBridge()
}

func (l *Local) InterruptDSHSession(ctx context.Context, req *machinev1.InterruptDSHSessionRequest) (*machinev1.InterruptDSHSessionResponse, error) {
	return l.n.InterruptDSHSession(ctx, req)
}

// ClaudeSessions and ReadClaudeSession are the Claude desktop app's Claude
// Code sessions here (claudesessions.go).
func (l *Local) ClaudeSessions() (*machinev1.ListClaudeSessionsResponse, error) {
	return l.n.ClaudeSessions()
}

func (l *Local) ReadClaudeSession(req *machinev1.ReadClaudeSessionRequest) (*machinev1.ReadClaudeSessionResponse, error) {
	return l.n.ReadClaudeSession(req)
}

func (l *Local) ReadDSHSession(req *machinev1.ReadDSHSessionRequest) (*machinev1.ReadDSHSessionResponse, error) {
	return l.n.ReadDSHSession(req)
}

// PushRevocations applies the owner's revocations (and a longer root chain),
// as the owner's devices push them.
func (l *Local) PushRevocations(r *identityv1.RevocationList, chain *identityv1.RootChain) {
	if chain != nil {
		l.n.acceptRootChain(chain)
	}
	l.n.applyRevocations(r)
}

// RevokedGrants are the grants the owner's revocations name, as this
// machine applied them.
func (l *Local) RevokedGrants() []string {
	l.n.mu.Lock()
	defer l.n.mu.Unlock()
	return slices.Clone(l.n.revocations.GetRevokedGrantIds())
}

// ListProjects lists the projects, without their base branches (which the
// owner's screens do not show, and which cost a git call each).
func (l *Local) ListProjects(ctx context.Context) (*machinev1.ListProjectsResponse, error) {
	return (&machineService{n: l.n}).projects(ctx, l.p, false)
}

// ApplySettings applies a setting (a project, pausing, the name) as the
// owner's devices send it.
func (l *Local) ApplySettings(ctx context.Context, build func(*identityv1.AdminCommand)) (string, error) {
	cmd := &identityv1.AdminCommand{OwnerId: l.n.OwnerID(), CommandId: identity.RandomCode(16), TargetMachineId: l.n.machineID,
		ExpiresAt: timestamppb.New(time.Now().Add(5 * time.Minute))}
	build(cmd)
	return l.n.ApplySettings(ctx, cmd)
}

func (l *Local) ListSessions(req *sessionv1.ListSessionsRequest) (*sessionv1.ListSessionsResponse, error) {
	list, next, err := l.n.sessions.List(l.p, req)
	if err != nil {
		return nil, err
	}
	return &sessionv1.ListSessionsResponse{Sessions: list, Next: next}, nil
}

func (l *Local) WatchSessions(ctx context.Context, send func(*sessionv1.WatchSessionsResponse) error) error {
	return l.n.sessions.Watch(ctx, l.p, send)
}

func (l *Local) StreamEvents(ctx context.Context, req *sessionv1.StreamEventsRequest, send func(*sessionv1.SessionEvent) error) error {
	return l.n.sessions.Stream(ctx, l.p, req, send)
}

// Watch follows this machine (MachineService.Watch), in process.
func (l *Local) Watch(ctx context.Context, req *machinev1.WatchRequest, send func(*machinev1.WatchResponse) error) error {
	return l.n.Watch(ctx, l.p, req, send)
}

func (l *Local) ReadToolOutput(req *sessionv1.ReadToolOutputRequest, send func(*sessionv1.ToolOutputChunk) error) error {
	return l.n.sessions.ReadToolOutput(l.p, req, send)
}

func (l *Local) ListEvents(req *sessionv1.ListEventsRequest) (*sessionv1.ListEventsResponse, error) {
	return l.n.sessions.ListEvents(l.p, req)
}

// Get is one session, as the owner sees it.
func (l *Local) Get(id string) (*sessionv1.Session, error) {
	return l.n.sessions.Get(l.p, id)
}

// Start starts a session as the owner (created_by "owner").
func (l *Local) Start(ctx context.Context, req *sessionv1.StartSessionRequest) (*sessionv1.Session, error) {
	return l.n.sessions.Start(ctx, l.p, req)
}

// Send sends a message to a session the owner started.
func (l *Local) Send(ctx context.Context, id, text string, atts []*sessionv1.Attachment) (*sessionv1.Session, error) {
	return l.n.sessions.Send(ctx, l.p, id, text, atts)
}

func (l *Local) Interrupt(ctx context.Context, id string) (*sessionv1.Session, error) {
	return l.n.sessions.Interrupt(ctx, l.p, id)
}

func (l *Local) Archive(ctx context.Context, id string) (*sessionv1.Session, error) {
	return l.n.sessions.Archive(ctx, l.p, id)
}

func (l *Local) Unarchive(ctx context.Context, id string) (*sessionv1.Session, error) {
	return l.n.sessions.Unarchive(ctx, l.p, id)
}

func (l *Local) Delete(ctx context.Context, id string) error {
	return l.n.sessions.Delete(ctx, l.p, id)
}

func (l *Local) Rename(ctx context.Context, id, title string) (*sessionv1.Session, error) {
	return l.n.sessions.Rename(ctx, l.p, id, title)
}

func (l *Local) MarkSeen(ctx context.Context, req *sessionv1.MarkSessionSeenRequest) (*sessionv1.Session, error) {
	return l.n.sessions.MarkSeen(ctx, l.p, req)
}

func (l *Local) AnswerQuestion(ctx context.Context, req *sessionv1.AnswerQuestionRequest) (*sessionv1.Session, error) {
	return l.n.sessions.AnswerQuestion(ctx, l.p, req)
}

func (l *Local) Diff(ctx context.Context, req *sessionv1.GetDiffRequest) (*sessionv1.GetDiffResponse, error) {
	return l.n.sessions.Diff(ctx, l.p, req)
}

func (l *Local) Insights(ctx context.Context, req *sessionv1.GetSessionInsightsRequest) (*sessionv1.SessionInsights, error) {
	return l.n.sessions.Insights(ctx, l.p, req)
}

func (l *Local) Push(ctx context.Context, id string) (*sessionv1.PushSessionResponse, error) {
	return l.n.sessions.Push(ctx, l.p, id)
}

func (l *Local) Rollback(ctx context.Context, id string, turn uint32) (*sessionv1.Session, error) {
	return l.n.sessions.Rollback(ctx, l.p, id, turn)
}

// WorkSeq is where this machine's work counts are (work.go).
func (l *Local) WorkSeq() int64 { return l.n.st.WorkSeq() }

// CountWork adds up this machine's work counts over a range (work.go), in
// process: the owner device it runs on reads them here, never copies them.
func (l *Local) CountWork(ctx context.Context, buckets []workstats.Bound, whole workstats.Bound) (sessions, turns []workstats.Row, keys map[int64]WorkKey, err error) {
	return l.n.st.CountWork(ctx, buckets, whole)
}

// kClients: the agents that reached this machine, by client id, with the
// name and grant their owner-signed grant gave them (JSON), so the app on a
// machine only can name who started a session.
const kClients = "clients"

type knownClient struct {
	Label   string `json:"label"`
	GrantID string `json:"grant_id"`
	// Seen: when it last reached this machine (Unix seconds), kept to the
	// minute or so.
	Seen int64 `json:"seen,omitempty"`
}

func (n *Machine) knownClients() map[string]knownClient {
	m := map[string]knownClient{}
	if b, _ := n.st.Get(kClients); len(b) > 0 {
		json.Unmarshal(b, &m)
	}
	return m
}

// rememberClient keeps an agent's name and grant as its grant says, and
// when it came, when they are new or the time is a minute old.
func (n *Machine) rememberClient(clientID string, g *identityv1.Grant) {
	if g == nil {
		return
	}
	n.clientsMu.Lock()
	defer n.clientsMu.Unlock()
	m := n.knownClients()
	now := time.Now().Unix()
	c := knownClient{Label: g.ClientLabel, GrantID: g.GrantId, Seen: now}
	if old := m[clientID]; old.Label == c.Label && old.GrantID == c.GrantID && now-old.Seen < 60 {
		return
	}
	m[clientID] = c
	b, _ := json.Marshal(m)
	n.st.Set(kClients, b)
}

// KnownClient is an agent that reached this machine.
type KnownClient struct {
	ID, GrantID, Label string
	Seen               time.Time // zero when not recorded (before it was)
}

// Clients are the agents that reached this machine.
func (l *Local) Clients() []KnownClient {
	var out []KnownClient
	for id, c := range l.n.knownClients() {
		k := KnownClient{ID: id, GrantID: c.GrantID, Label: c.Label}
		if c.Seen > 0 {
			k.Seen = time.Unix(c.Seen, 0)
		}
		out = append(out, k)
	}
	return out
}

// InterruptActive stops every turn under way (running, or waiting for an
// answer), as the owner would.
func (l *Local) InterruptActive(ctx context.Context) {
	list, _, err := l.n.sessions.List(l.p, &sessionv1.ListSessionsRequest{})
	if err != nil {
		return
	}
	for _, s := range list {
		switch s.Status {
		case sessionv1.SessionStatus_SESSION_STATUS_QUEUED, sessionv1.SessionStatus_SESSION_STATUS_PREPARING,
			sessionv1.SessionStatus_SESSION_STATUS_RUNNING, sessionv1.SessionStatus_SESSION_STATUS_WAITING_INPUT:
			if _, err := l.n.sessions.Interrupt(ctx, l.p, s.SessionId); err != nil {
				l.n.log.Warn("interrupting a session before leaving", "session", s.SessionId, "err", err)
			}
		}
	}
}

// LeaveOwner has the relay remove this machine from its owner, as a
// trusted device would.
func (n *Machine) LeaveOwner(ctx context.Context) error {
	d := &transport.Dialer{RelayURL: n.RelayURL(), Role: relayv1.Role_ROLE_MACHINE, Key: n.key, Credentials: n.credentials, HTTPClient: n.cfg.HTTPClient}
	conn, err := d.Dial(ctx)
	if gone(err) {
		return nil // removed already
	}
	if err != nil {
		return err
	}
	defer conn.Close()
	c, err := n.control(conn)
	if err == nil {
		_, err = c.Leave(ctx, &relayv1.LeaveRequest{})
	}
	if err == nil {
		return nil
	}
	// The relay drops this machine's connections as it removes it, possibly
	// before the answer arrives: being refused now says it did.
	again, derr := d.Dial(ctx)
	if derr == nil {
		again.Close()
	}
	if gone(derr) {
		return nil
	}
	return err
}

// Retire forgets the owner of the machine in cfg.DataDir (which must not be
// running) and retires its key: it comes back under a new key, so a new
// machine id, with its projects and sessions. A removed machine's id never
// comes back to its owner; a new one can. The old key stays in
// keys/retired, for the audit log's checkpoints it signed.
func Retire(cfg Config) error {
	n, err := Open(cfg)
	if err != nil {
		return err
	}
	old := n.machineID
	err = n.Unpair()
	if err == nil {
		n.audit.Log(AuditRecord{Kind: "retired", Subject: old, Detail: "the machine key is replaced; this machine gets a new id"})
	}
	n.Close()
	if err != nil {
		return err
	}
	dir := filepath.Join(filepath.Dir(KeyPath(cfg.DataDir)), "retired")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	return os.Rename(KeyPath(cfg.DataDir), filepath.Join(dir, old+".key"))
}

// retiredKeys are the keys this machine had before (Retire).
func retiredKeys(dataDir string) []identity.Signer {
	paths, _ := filepath.Glob(filepath.Join(filepath.Dir(KeyPath(dataDir)), "retired", "*.key"))
	var keys []identity.Signer
	for _, p := range paths {
		if k, err := identity.LoadKey(p); err == nil {
			keys = append(keys, k)
		}
	}
	return keys
}

// Refused reports whether the relay refused this machine the last time it
// connected because it is no longer its owner's: the owner removed it, or
// is gone. Other refusals (a relay being reset, a passing failure) are not
// that.
func (n *Machine) Refused() bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.refused
}

// gone reports whether err is the relay refusing a machine it no longer
// counts as its owner's (pkg/relay's admission errors).
func gone(err error) bool {
	if !errors.Is(err, transport.ErrAdmission) {
		return false
	}
	msg := err.Error()
	for _, s := range []string{"machine removed", "unknown or removed machine", "no longer serves this device's owner", "does not serve that owner"} {
		if strings.Contains(msg, s) {
			return true
		}
	}
	return false
}

// Adopt takes a longer root chain and a newer cert for this machine's key,
// issued by one of the owner's devices in this process (a machine only
// becoming a trusted device), without waiting for the relay to pass them on.
func (n *Machine) Adopt(chain *identityv1.RootChain, cert *identityv1.Signed) {
	n.acceptRootChain(chain)
	n.acceptMachineCert(cert)
}

// Unpair forgets the owner, so the machine can pair again; its key,
// projects and sessions stay.
func (n *Machine) Unpair() error {
	for _, k := range []string{kOwnerID, kRelayURL, kMachineCert, kGrants} {
		if err := n.st.Set(k, []byte{}); err != nil {
			return err
		}
	}
	n.mu.Lock()
	n.ownerID, n.relayURL, n.trust, n.machineCert, n.refused, n.grants = "", "", nil, nil, false, nil
	n.mu.Unlock()
	n.audit.Log(AuditRecord{Kind: "unpaired"})
	return nil
}
