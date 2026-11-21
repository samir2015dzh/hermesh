package owner

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	identityv1 "github.com/samir2015dzh/aide/gen/go/aide/identity/v1"
	localv1 "github.com/samir2015dzh/aide/gen/go/aide/local/v1"
	"github.com/samir2015dzh/aide/gen/go/aide/local/v1/localv1connect"
	machinev1 "github.com/samir2015dzh/aide/gen/go/aide/machine/v1"
	sessionv1 "github.com/samir2015dzh/aide/gen/go/aide/session/v1"
	"github.com/samir2015dzh/aide/internal/localipc"
	"github.com/samir2015dzh/aide/internal/machine"
	"github.com/samir2015dzh/aide/internal/resident"
)

// ServeLocal serves LocalService on a local endpoint that only the same
// user may connect to. It blocks until ctx is done.
func (d *Device) ServeLocal(ctx context.Context, sockPath string) error {
	ln, err := localipc.Listen(sockPath)
	if err != nil {
		return err
	}
	var protocols http.Protocols
	protocols.SetHTTP1(true)
	protocols.SetUnencryptedHTTP2(true)
	srv := &http.Server{Handler: d.LocalHandler(), Protocols: &protocols}
	go func() {
		<-ctx.Done()
		srv.Close()
	}()
	err = srv.Serve(ln)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

// LocalHandler serves LocalService over HTTP, for a caller that provides
// its own listener and access control (the iPhone app's loopback server).
func (d *Device) LocalHandler() http.Handler {
	mux := http.NewServeMux()
	mux.Handle(localv1connect.NewLocalServiceHandler(&localService{d: d}, connect.WithInterceptors(connect.UnaryInterceptorFunc(d.kickAfterWrites))))
	return viewers(mux)
}

// ViewerHeader marks the calls of a window of the app (the desktop app's
// gateway sets it; its main process's own Watch, for notifications and the
// notification area, has none): while one holds a stream open, aided does
// not save power (internal/resident).
const ViewerHeader = "Aide-Viewer"

// AppHeader is the desktop app's main process's pid, on its own Watch: when
// power saving starts, the app's processes give back their idle memory with
// aided's (internal/resident).
const AppHeader = "Aide-App-Pid"

func viewers(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if pid, err := strconv.Atoi(r.Header.Get(AppHeader)); err == nil {
			defer resident.SetApp(pid)()
		}
		if r.Header.Get(ViewerHeader) != "" {
			defer resident.Viewing()()
			defer windowViewing()()
			r = r.WithContext(context.WithValue(r.Context(), viewerKey{}, true))
		}
		next.ServeHTTP(w, r)
	})
}

type viewerKey struct{}

// fromWindow says whether a call is a window's (ViewerHeader).
func fromWindow(ctx context.Context) bool { return ctx.Value(viewerKey{}) != nil }

// windows counts the calls of windows in flight; arrived is closed when the
// first one comes (docs/resident.md §4: what runs only for a window looking
// runs at once, then at its pace).
var windows struct {
	mu      sync.Mutex
	n       int
	arrived chan struct{}
}

func windowViewing() (done func()) {
	windows.mu.Lock()
	windows.n++
	if windows.n == 1 && windows.arrived != nil {
		close(windows.arrived)
		windows.arrived = nil
	}
	windows.mu.Unlock()
	return func() {
		windows.mu.Lock()
		windows.n--
		windows.mu.Unlock()
	}
}

// windowLooking says whether a window holds a call open now, and gives a
// channel closed once one does, when none does.
func windowLooking() (bool, <-chan struct{}) {
	windows.mu.Lock()
	defer windows.mu.Unlock()
	if windows.n > 0 {
		return true, nil
	}
	if windows.arrived == nil {
		windows.arrived = make(chan struct{})
	}
	return false, windows.arrived
}

// LocalMachine is this device's own machine while it runs, else nil.
func (d *Device) LocalMachine() *machine.Machine {
	o := d.owner()
	if o == nil {
		return nil
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.localMachine
}

// kickAfterWrites tells the screens (Watch) to look again after a call that
// may have changed what they show (writes).
func (d *Device) kickAfterWrites(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		res, err := next(ctx, req)
		if writes(req.Spec().Procedure) {
			d.mu.Lock()
			for _, o := range d.profiles {
				o.rep.mu.Lock()
				o.rep.kickLocked()
				o.rep.mu.Unlock()
			}
			d.mu.Unlock()
		}
		return res, err
	}
}

// writes says whether a call may change what the screens show: any but a
// read, and but talking to Her, moving her files and dropping a message
// kept for her, which they don't show.
func writes(procedure string) bool {
	name := procedure[strings.LastIndex(procedure, "/")+1:]
	switch name {
	case "HerCall", "HerSend", "HerUpload", "HerDownload", "DeleteHerWaiting":
		return false
	}
	return !strings.HasPrefix(name, "Get") && !strings.HasPrefix(name, "List")
}

// localService serves the current profile (profiles.go).
type localService struct{ d *Device }

func (l *localService) o() *Owner { return l.d.owner() }

func (l *localService) RegisterPush(ctx context.Context, req *localv1.RegisterPushRequest) (*localv1.RegisterPushResponse, error) {
	return &localv1.RegisterPushResponse{}, l.o().RegisterPush(ctx, req)
}

func (l *localService) StopScreen(context.Context, *localv1.StopScreenRequest) (*localv1.StopScreenResponse, error) {
	return nil, errRemoteDesktop
}

// errRemoteDesktop answers the remote desktop's calls, which the protocol keeps.
var errRemoteDesktop = connect.NewError(connect.CodeUnimplemented, errors.New("remote desktop was removed"))

// GetStatus never waits on the relay: RelayConnected is the connection the
// owner keeps (stayConnected).
func (l *localService) GetStatus(ctx context.Context, _ *localv1.GetStatusRequest) (*localv1.GetStatusResponse, error) {
	return l.o().Status(), nil
}

func (l *localService) ListProfiles(context.Context, *localv1.ListProfilesRequest) (*localv1.ListProfilesResponse, error) {
	return &localv1.ListProfilesResponse{Profiles: l.d.ListProfiles()}, nil
}

func (l *localService) SwitchProfile(_ context.Context, req *localv1.SwitchProfileRequest) (*localv1.SwitchProfileResponse, error) {
	return &localv1.SwitchProfileResponse{}, l.d.SwitchProfile(req.Id)
}

func (l *localService) AddProfile(context.Context, *localv1.AddProfileRequest) (*localv1.AddProfileResponse, error) {
	id, err := l.d.AddProfile()
	return &localv1.AddProfileResponse{Id: id}, err
}

func (l *localService) SignOut(ctx context.Context, _ *localv1.SignOutRequest) (*localv1.SignOutResponse, error) {
	return &localv1.SignOutResponse{}, l.d.SignOut(ctx)
}

func (l *localService) SignIn(ctx context.Context, req *localv1.SignInRequest) (*localv1.SignInResponse, error) {
	return &localv1.SignInResponse{}, l.d.SignIn(ctx, req)
}

func (l *localService) Setup(ctx context.Context, req *localv1.SetupRequest) (*localv1.SetupResponse, error) {
	return l.o().Setup(ctx, req)
}

func (l *localService) LogIn(ctx context.Context, req *localv1.LogInRequest) (*localv1.LogInResponse, error) {
	if owner := l.o().serverOwner(ctx, req.RelayUrl); owner != "" {
		if err := l.d.checkNewOwner(owner); err != nil {
			return nil, err
		}
	}
	return l.o().LogIn(ctx, req)
}

func (l *localService) GetLogin(ctx context.Context, _ *localv1.GetLoginRequest) (*localv1.GetLoginResponse, error) {
	return l.o().GetLogin(ctx)
}

func (l *localService) SetLogin(ctx context.Context, req *localv1.SetLoginRequest) (*localv1.SetLoginResponse, error) {
	res, err := l.o().SetLogin(ctx, req)
	l.o().wrote(ctx, err, true)
	return res, err
}

func (l *localService) ListDevices(ctx context.Context, _ *localv1.ListDevicesRequest) (*localv1.ListDevicesResponse, error) {
	d, err := l.o().ListDevices(ctx)
	return &localv1.ListDevicesResponse{Devices: d}, err
}

func (l *localService) RemoveDevice(ctx context.Context, req *localv1.RemoveDeviceRequest) (*localv1.RemoveDeviceResponse, error) {
	res, err := l.o().RemoveDevice(ctx, req.KeyId, req.EraseHer)
	l.o().wrote(ctx, err, true)
	return res, err
}

func (l *localService) Leave(ctx context.Context, _ *localv1.LeaveRequest) (*localv1.LeaveResponse, error) {
	return &localv1.LeaveResponse{}, l.d.Leave(ctx)
}

func (l *localService) EraseEverything(ctx context.Context, _ *localv1.EraseEverythingRequest) (*localv1.EraseEverythingResponse, error) {
	id, err := l.d.EraseEverything(ctx)
	if err != nil {
		return nil, err
	}
	return &localv1.EraseEverythingResponse{EraseId: id}, nil
}

func (l *localService) EraseStatus(ctx context.Context, req *localv1.EraseStatusRequest) (*localv1.EraseStatusResponse, error) {
	return l.o().EraseStatus(ctx, req.EraseId)
}

func (l *localService) FinishErase(ctx context.Context, _ *localv1.FinishEraseRequest) (*localv1.FinishEraseResponse, error) {
	return &localv1.FinishEraseResponse{}, l.d.FinishErase(ctx)
}

func (l *localService) SetLocalMachine(ctx context.Context, req *localv1.SetLocalMachineRequest) (*localv1.SetLocalMachineResponse, error) {
	id, err := l.o().SetLocalMachine(ctx, req.Enabled, req.MachineName)
	l.o().wrote(ctx, err, true)
	if err != nil {
		return nil, err
	}
	return &localv1.SetLocalMachineResponse{MachineId: id}, nil
}

func (l *localService) ListMachines(ctx context.Context, _ *localv1.ListMachinesRequest) (*localv1.ListMachinesResponse, error) {
	if m, ok, err := l.o().onlyMachine(); ok {
		if err != nil {
			return nil, err
		}
		machines := l.o().onlyMachineList(m)
		return &localv1.ListMachinesResponse{Machines: machines}, nil
	}
	machines, err := l.o().machines(ctx)
	if err != nil {
		return nil, err
	}
	l.o().machineStatus(machines)
	return &localv1.ListMachinesResponse{Machines: machines}, nil
}

func (l *localService) RenameMachine(ctx context.Context, req *localv1.RenameMachineRequest) (*localv1.RenameMachineResponse, error) {
	if m, ok, err := l.o().onlyMachine(); ok {
		var name string
		if err == nil {
			name, err = renameTo(req.MachineName)
		}
		if err == nil {
			err = onlyMachineSettings(ctx, m, req.MachineId, func(c *identityv1.AdminCommand) {
				c.Command = &identityv1.AdminCommand_SetName{SetName: &identityv1.SetName{Name: name}}
			})
		}
		return &localv1.RenameMachineResponse{}, err
	}
	err := l.o().RenameMachine(ctx, req.MachineId, req.MachineName)
	l.o().wrote(ctx, err, true, req.MachineId)
	return &localv1.RenameMachineResponse{}, err
}

func (l *localService) SetMachinePaused(ctx context.Context, req *localv1.SetMachinePausedRequest) (*localv1.SetMachinePausedResponse, error) {
	if m, ok, err := l.o().onlyMachine(); ok {
		if err == nil {
			err = onlyMachineSettings(ctx, m, req.MachineId, func(c *identityv1.AdminCommand) {
				c.Command = &identityv1.AdminCommand_SetPaused{SetPaused: &identityv1.SetPaused{Paused: req.Paused}}
			})
		}
		return &localv1.SetMachinePausedResponse{}, err
	}
	err := l.o().SetMachinePaused(ctx, req.MachineId, req.Paused)
	l.o().wrote(ctx, err, false, req.MachineId)
	return &localv1.SetMachinePausedResponse{}, err
}

func (l *localService) SetAgentDefaults(ctx context.Context, req *localv1.SetAgentDefaultsRequest) (*localv1.SetAgentDefaultsResponse, error) {
	if req.Defaults == nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("no defaults"))
	}
	if m, ok, err := l.o().onlyMachine(); ok {
		if err == nil {
			err = onlyMachineSettings(ctx, m, req.MachineId, func(c *identityv1.AdminCommand) {
				c.Command = &identityv1.AdminCommand_SetAgentDefaults{SetAgentDefaults: req.Defaults}
			})
		}
		return &localv1.SetAgentDefaultsResponse{}, err
	}
	err := l.o().SetAgentDefaults(ctx, req.MachineId, req.Defaults)
	l.o().wrote(ctx, err, false, req.MachineId)
	return &localv1.SetAgentDefaultsResponse{}, err
}

// CheckAgents checks a machine's coding agents again: another machine end
// to end, waiting for it; this device's own in the background.
func (l *localService) CheckAgents(ctx context.Context, req *localv1.CheckAgentsRequest) (*localv1.CheckAgentsResponse, error) {
	if req.MachineId != "" && (l.o().local() == nil || l.o().local().MachineID() != req.MachineId) {
		return &localv1.CheckAgentsResponse{}, l.o().RecheckAgents(ctx, req.MachineId, req.RescanHidden)
	}
	m, ok, err := l.o().onlyMachine()
	if !ok {
		m = l.o().local()
	}
	if err == nil && m == nil {
		err = connect.NewError(connect.CodeFailedPrecondition, errors.New("this computer shares no coding agents"))
	}
	if err != nil {
		return nil, err
	}
	if req.RescanHidden {
		m.RescanAgents()
	} else {
		m.CheckAgents()
	}
	return &localv1.CheckAgentsResponse{}, nil
}

// SetAgentHidden hides a coding agent a machine lists, or shows it again.
func (l *localService) SetAgentHidden(ctx context.Context, req *localv1.SetAgentHiddenRequest) (*localv1.SetAgentHiddenResponse, error) {
	if req.Agent == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("which coding agent?"))
	}
	if m, ok, err := l.o().onlyMachine(); ok {
		if err == nil {
			err = onlyMachineSettings(ctx, m, req.MachineId, func(c *identityv1.AdminCommand) {
				c.Command = &identityv1.AdminCommand_SetAgentHidden{SetAgentHidden: &identityv1.SetAgentHidden{Agent: req.Agent, Hidden: req.Hidden}}
			})
		}
		return &localv1.SetAgentHiddenResponse{}, err
	}
	err := l.o().SetAgentHidden(ctx, req.MachineId, req.Agent, req.Hidden)
	l.o().wrote(ctx, err, false, req.MachineId)
	return &localv1.SetAgentHiddenResponse{}, err
}

func (l *localService) ListMachineRepos(ctx context.Context, req *localv1.ListMachineReposRequest) (*localv1.ListMachineReposResponse, error) {
	repos, err := l.o().MachineRepos(ctx, req.MachineId)
	return &localv1.ListMachineReposResponse{Repos: repos}, err
}

func (l *localService) ListMachineFolders(ctx context.Context, req *localv1.ListMachineFoldersRequest) (*machinev1.ListFoldersResponse, error) {
	return l.o().MachineFolders(ctx, req.MachineId, req.Path, req.Hidden)
}

func (l *localService) TestMachineAgent(ctx context.Context, req *localv1.TestMachineAgentRequest) (*machinev1.TestAgentResponse, error) {
	return l.o().TestMachineAgent(ctx, req.MachineId, req.Agent)
}

func (l *localService) ListMachineCustomAgents(ctx context.Context, req *localv1.ListMachineCustomAgentsRequest) (*machinev1.ListCustomAgentsResponse, error) {
	return l.o().MachineCustomAgents(ctx, req.MachineId)
}

func (l *localService) SaveMachineCustomAgent(ctx context.Context, req *localv1.SaveMachineCustomAgentRequest) (*machinev1.SaveCustomAgentResponse, error) {
	return &machinev1.SaveCustomAgentResponse{}, l.o().SaveMachineCustomAgent(ctx, req.MachineId, req.Agent)
}

func (l *localService) RemoveMachineCustomAgent(ctx context.Context, req *localv1.RemoveMachineCustomAgentRequest) (*machinev1.RemoveCustomAgentResponse, error) {
	return &machinev1.RemoveCustomAgentResponse{}, l.o().RemoveMachineCustomAgent(ctx, req.MachineId, req.Name)
}

func (l *localService) TestMachineCustomAgent(ctx context.Context, req *localv1.TestMachineCustomAgentRequest) (*machinev1.TestCustomAgentResponse, error) {
	return l.o().TestMachineCustomAgent(ctx, req.MachineId, req.Agent)
}

func (l *localService) ListExecLog(ctx context.Context, req *localv1.ListExecLogRequest) (*localv1.ListExecLogResponse, error) {
	return l.o().ListExecLog(ctx, req.MachineId)
}

// ListDSHSessions and ReadDSHSession: on a Mac that is only a machine, its
// own.
func (l *localService) ListDSHSessions(ctx context.Context, req *localv1.ListDSHSessionsRequest) (*localv1.ListDSHSessionsResponse, error) {
	if m, ok, err := l.o().onlyMachine(); ok {
		if err != nil {
			return nil, err
		}
		res, err := m.DSHSessions(ctx)
		if err != nil {
			return nil, err
		}
		info := m.Info()
		out := &localv1.ListDSHSessionsResponse{Sessions: dshEntries(res, info.MachineId, info.MachineName, time.Now())}
		if res.Bridge != nil || res.DshHome != "" {
			out.Bridges = []*localv1.DSHBridgeEntry{{MachineId: info.MachineId, Bridge: res.Bridge, DshHome: res.DshHome}}
		}
		return out, nil
	}
	return l.o().ListDSHSessions(ctx, req.MachineId)
}

// ListClaudeSessions and ReadClaudeSession: on a Mac that is only a
// machine, its own.
func (l *localService) ListClaudeSessions(ctx context.Context, req *localv1.ListClaudeSessionsRequest) (*localv1.ListClaudeSessionsResponse, error) {
	if m, ok, err := l.o().onlyMachine(); ok {
		if err != nil {
			return nil, err
		}
		res, err := m.ClaudeSessions()
		if err != nil {
			return nil, err
		}
		info := m.Info()
		out := &localv1.ListClaudeSessionsResponse{Indexes: []*localv1.ClaudeIndexEntry{{MachineId: info.MachineId, Indexes: res.Indexes}}}
		for _, s := range res.Sessions {
			out.Sessions = append(out.Sessions, &localv1.ClaudeSessionEntry{MachineId: info.MachineId, MachineName: info.MachineName, Session: s, ReadAt: timestamppb.Now()})
		}
		return out, nil
	}
	return l.o().ListClaudeSessions(ctx, req.MachineId)
}

func (l *localService) ReadClaudeSession(ctx context.Context, req *localv1.ReadClaudeSessionRequest) (*machinev1.ReadClaudeSessionResponse, error) {
	if m, ok, err := l.o().onlyMachine(); ok {
		if err != nil {
			return nil, err
		}
		return m.ReadClaudeSession(&machinev1.ReadClaudeSessionRequest{SessionId: req.SessionId, AfterSeq: req.AfterSeq, BeforeSeq: req.BeforeSeq, Limit: req.Limit, View: req.View})
	}
	return l.o().ReadClaudeSession(ctx, req)
}

func (l *localService) ReadDSHSession(ctx context.Context, req *localv1.ReadDSHSessionRequest) (*machinev1.ReadDSHSessionResponse, error) {
	if m, ok, err := l.o().onlyMachine(); ok {
		if err != nil {
			return nil, err
		}
		return m.ReadDSHSession(dshRead(req))
	}
	return l.o().ReadDSHSession(ctx, req)
}

// SetDSHTakeover, SendDSHMessage and InterruptDSHSession: on a Mac that is
// only a machine, its own.
func (l *localService) SetDSHTakeover(ctx context.Context, req *localv1.SetDSHTakeoverRequest) (*machinev1.SetDSHTakeoverResponse, error) {
	if m, ok, err := l.o().onlyMachine(); ok {
		if err != nil {
			return nil, err
		}
		return m.SetDSHTakeover(&machinev1.SetDSHTakeoverRequest{SessionId: req.SessionId, On: req.On})
	}
	return l.o().SetDSHTakeover(ctx, req)
}

func (l *localService) SendDSHMessage(ctx context.Context, req *localv1.SendDSHMessageRequest) (*machinev1.SendDSHMessageResponse, error) {
	if m, ok, err := l.o().onlyMachine(); ok {
		if err != nil {
			return nil, err
		}
		return m.SendDSHMessage(ctx, dshSend(req))
	}
	return l.o().SendDSHMessage(ctx, req)
}

func (l *localService) InterruptDSHSession(ctx context.Context, req *localv1.InterruptDSHSessionRequest) (*machinev1.InterruptDSHSessionResponse, error) {
	if m, ok, err := l.o().onlyMachine(); ok {
		if err != nil {
			return nil, err
		}
		return m.InterruptDSHSession(ctx, &machinev1.InterruptDSHSessionRequest{SessionId: req.SessionId})
	}
	return l.o().InterruptDSHSession(ctx, req)
}

func (l *localService) DSHSessionOptions(ctx context.Context, req *localv1.DSHSessionOptionsRequest) (*machinev1.DSHSessionOptionsResponse, error) {
	if m, ok, err := l.o().onlyMachine(); ok {
		if err != nil {
			return nil, err
		}
		return m.DSHSessionOptions(ctx, req.SessionId)
	}
	return l.o().DSHSessionOptions(ctx, req)
}

func (l *localService) PrepareDSHBridge(ctx context.Context, req *localv1.PrepareDSHBridgeRequest) (*machinev1.PrepareDSHBridgeResponse, error) {
	if m, ok, err := l.o().onlyMachine(); ok {
		if err != nil {
			return nil, err
		}
		return m.PrepareDSHBridge()
	}
	return l.o().PrepareDSHBridge(ctx, req)
}

// GetDiagnostics describes this Mac: what it is to its owner, then its
// machine, when it shares its coding agents.
func (l *localService) GetDiagnostics(ctx context.Context, _ *localv1.GetDiagnosticsRequest) (*localv1.GetDiagnosticsResponse, error) {
	st := l.o().Status()
	role := "not set up"
	switch {
	case st.MachineOnly:
		role = "machine only"
	case st.Initialized:
		role = "device"
	}
	text := fmt.Sprintf("this device  %s, owner %s\n", role, st.OwnerId)
	m, ok, err := l.o().onlyMachine()
	if !ok {
		m, err = l.o().local(), nil
	}
	switch {
	case err != nil:
		text += "its machine  not running: " + err.Error() + "\n"
	case m == nil:
		text += "its machine  none: it shares no coding agents\n"
	default:
		text += m.Diagnostics()
	}
	return &localv1.GetDiagnosticsResponse{Text: text}, nil
}

func (l *localService) RemoveMachine(ctx context.Context, req *localv1.RemoveMachineRequest) (*localv1.RemoveMachineResponse, error) {
	err := l.o().RemoveMachine(ctx, req.MachineId, req.EraseHer)
	l.o().wrote(ctx, err, true)
	return &localv1.RemoveMachineResponse{}, err
}

func (l *localService) ListProjects(ctx context.Context, req *localv1.ListProjectsRequest) (*localv1.ListProjectsResponse, error) {
	if m, ok, err := l.o().onlyMachine(); ok {
		if err == nil {
			err = onlyThisMachine(m, req.MachineId)
		}
		if err != nil {
			return nil, err
		}
		return onlyMachineProjects(ctx, m)
	}
	return l.o().ListProjects(ctx, req.MachineId)
}

func (l *localService) AddProject(ctx context.Context, req *localv1.AddProjectRequest) (*localv1.AddProjectResponse, error) {
	if m, ok, err := l.o().onlyMachine(); ok {
		if err == nil {
			err = onlyMachineSettings(ctx, m, req.MachineId, func(c *identityv1.AdminCommand) {
				c.Command = &identityv1.AdminCommand_AddProject{AddProject: &identityv1.AddProject{ProjectId: req.ProjectId, Path: req.Path, DefaultBaseRef: req.DefaultBaseRef,
					Workspace: req.Workspace, Description: req.Description, Agents: req.Agents}}
			})
		}
		return &localv1.AddProjectResponse{}, err
	}
	err := l.o().AddProject(ctx, req)
	l.o().wrote(ctx, err, false, req.MachineId)
	return &localv1.AddProjectResponse{}, err
}

func (l *localService) RemoveProject(ctx context.Context, req *localv1.RemoveProjectRequest) (*localv1.RemoveProjectResponse, error) {
	if m, ok, err := l.o().onlyMachine(); ok {
		if err == nil {
			err = onlyMachineSettings(ctx, m, req.MachineId, func(c *identityv1.AdminCommand) {
				c.Command = &identityv1.AdminCommand_RemoveProject{RemoveProject: &identityv1.RemoveProject{ProjectId: req.ProjectId}}
			})
		}
		return &localv1.RemoveProjectResponse{}, err
	}
	err := l.o().RemoveProject(ctx, req.MachineId, req.ProjectId)
	l.o().wrote(ctx, err, false, req.MachineId)
	return &localv1.RemoveProjectResponse{}, err
}

func (l *localService) ListClients(ctx context.Context, _ *localv1.ListClientsRequest) (*localv1.ListClientsResponse, error) {
	if m, ok, err := l.o().onlyMachine(); ok {
		if err != nil {
			return nil, err
		}
		return &localv1.ListClientsResponse{Clients: onlyMachineClients(m)}, nil
	}
	clients, err := l.o().ListClients(ctx)
	if err != nil {
		return nil, err
	}
	return &localv1.ListClientsResponse{Clients: clients}, nil
}

func (l *localService) RenameClient(ctx context.Context, req *localv1.RenameClientRequest) (*localv1.RenameClientResponse, error) {
	v, err := l.o().RenameClient(ctx, req.GrantId, req.Label, req.Icon, req.Image)
	l.o().wrote(ctx, err, true)
	if err != nil {
		return nil, err
	}
	return &localv1.RenameClientResponse{GrantVersion: v}, nil
}

func (l *localService) UpdateClient(ctx context.Context, req *localv1.UpdateClientRequest) (*localv1.UpdateClientResponse, error) {
	if req.Spec == nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("missing spec"))
	}
	v, acks, err := l.o().UpdateClient(ctx, req.GrantId, req.Spec)
	l.o().wrote(ctx, err, true)
	if err != nil {
		return nil, err
	}
	return &localv1.UpdateClientResponse{GrantVersion: v, Acks: acks}, nil
}

func (l *localService) RevokeClient(ctx context.Context, req *localv1.RevokeClientRequest) (*localv1.RevokeClientResponse, error) {
	acks, err := l.o().RevokeClient(ctx, req.GrantId)
	l.o().wrote(ctx, err, true)
	if err != nil {
		return nil, err
	}
	return &localv1.RevokeClientResponse{Acks: acks}, nil
}

func (l *localService) ListSessions(ctx context.Context, req *localv1.ListSessionsRequest) (*localv1.ListSessionsResponse, error) {
	if m, ok, err := l.o().onlyMachine(); ok {
		if err != nil {
			return nil, err
		}
		res, err := m.ListSessions(&sessionv1.ListSessionsRequest{Limit: req.Limit, Before: req.Before, View: req.View})
		if err != nil {
			return nil, err
		}
		return &localv1.ListSessionsResponse{Sessions: res.Sessions, Next: res.Next}, nil
	}
	return l.o().ListSessions(ctx, req)
}

func (l *localService) QueryWork(ctx context.Context, req *localv1.QueryWorkRequest) (*localv1.QueryWorkResponse, error) {
	if m, ok, err := l.o().onlyMachine(); ok {
		if err != nil {
			return nil, err
		}
		return QueryWorkOnly(ctx, m, req)
	}
	return l.o().QueryWork(ctx, req)
}

func (l *localService) WatchSessions(ctx context.Context, _ *localv1.WatchSessionsRequest, stream *connect.ServerStream[localv1.WatchSessionsResponse]) error {
	if m, ok, err := l.o().onlyMachine(); ok {
		if err != nil {
			return err
		}
		return onlyMachineWatch(ctx, m, stream.Send)
	}
	return l.o().WatchSessions(ctx, stream.Send)
}

func (l *localService) StreamSessionEvents(ctx context.Context, req *localv1.StreamSessionEventsRequest, stream *connect.ServerStream[sessionv1.SessionEvent]) error {
	if m, ok, err := l.o().onlyMachine(); ok {
		if err != nil {
			return err
		}
		return m.StreamEvents(ctx, &sessionv1.StreamEventsRequest{SessionId: req.SessionId, AfterSeq: req.AfterSeq, Follow: req.Follow, Partial: req.Partial, View: req.View}, stream.Send)
	}
	return l.o().StreamSessionEvents(ctx, req, stream.Send)
}

func (l *localService) ListSessionEvents(ctx context.Context, req *localv1.ListSessionEventsRequest) (*localv1.ListSessionEventsResponse, error) {
	list := l.o().ListSessionEvents
	if m, ok, err := l.o().onlyMachine(); ok {
		if err != nil {
			return nil, err
		}
		list = func(_ context.Context, r *sessionv1.ListEventsRequest) (*sessionv1.ListEventsResponse, error) {
			return m.ListEvents(r)
		}
	}
	res, err := list(ctx, &sessionv1.ListEventsRequest{SessionId: req.SessionId, AfterSeq: req.AfterSeq, BeforeSeq: req.BeforeSeq, Limit: req.Limit, View: req.View})
	if err != nil {
		return nil, err
	}
	return &localv1.ListSessionEventsResponse{Events: res.Events, HasMoreBefore: res.HasMoreBefore, HasMoreAfter: res.HasMoreAfter}, nil
}

func (l *localService) ReadToolOutput(ctx context.Context, req *localv1.ReadToolOutputRequest, stream *connect.ServerStream[sessionv1.ToolOutputChunk]) error {
	if m, ok, err := l.o().onlyMachine(); ok {
		if err != nil {
			return err
		}
		return m.ReadToolOutput(&sessionv1.ReadToolOutputRequest{SessionId: req.SessionId, CallId: req.CallId}, stream.Send)
	}
	return l.o().ReadToolOutput(ctx, req, stream.Send)
}

func (l *localService) AnswerQuestion(ctx context.Context, req *localv1.AnswerQuestionRequest) (*localv1.AnswerQuestionResponse, error) {
	answer := &sessionv1.AnswerQuestionRequest{SessionId: req.SessionId, QuestionId: req.QuestionId, Answers: req.Answers, Decline: req.Decline}
	answerer := l.o().AnswerQuestion
	if m, ok, err := l.o().onlyMachine(); ok {
		if err != nil {
			return nil, err
		}
		answerer = m.AnswerQuestion
	}
	s, err := answerer(ctx, answer)
	if err != nil {
		return nil, err
	}
	return &localv1.AnswerQuestionResponse{Session: s}, nil
}

func (l *localService) StartSession(ctx context.Context, req *localv1.StartSessionRequest) (*localv1.SessionResponse, error) {
	if req.Session == nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("no session to start"))
	}
	if m, ok, err := l.o().onlyMachine(); ok {
		if err != nil {
			return nil, err
		}
		return sessionResponse(m.Start(ctx, req.Session))
	}
	return sessionResponse(l.o().StartSession(ctx, req.MachineId, req.Session))
}

func (l *localService) SendMessage(ctx context.Context, req *localv1.SendMessageRequest) (*localv1.SessionResponse, error) {
	if m, ok, err := l.o().onlyMachine(); ok {
		if err != nil {
			return nil, err
		}
		return sessionResponse(m.Send(ctx, req.SessionId, req.Text, req.Attachments))
	}
	return sessionResponse(l.o().SendMessage(ctx, req.SessionId, req.Text, req.Attachments))
}

func (l *localService) InterruptSession(ctx context.Context, req *localv1.InterruptSessionRequest) (*localv1.SessionResponse, error) {
	if m, ok, err := l.o().onlyMachine(); ok {
		if err != nil {
			return nil, err
		}
		return sessionResponse(m.Interrupt(ctx, req.SessionId))
	}
	return sessionResponse(l.o().InterruptSession(ctx, req.SessionId))
}

func (l *localService) ArchiveSession(ctx context.Context, req *localv1.ArchiveSessionRequest) (*localv1.SessionResponse, error) {
	if m, ok, err := l.o().onlyMachine(); ok {
		if err != nil {
			return nil, err
		}
		return sessionResponse(m.Archive(ctx, req.SessionId))
	}
	return sessionResponse(l.o().ArchiveSession(ctx, req.SessionId))
}

func (l *localService) DeleteSession(ctx context.Context, req *localv1.DeleteSessionRequest) (*localv1.DeleteSessionResponse, error) {
	if m, ok, err := l.o().onlyMachine(); ok {
		if err == nil {
			err = m.Delete(ctx, req.SessionId)
		}
		if err != nil {
			return nil, err
		}
		return &localv1.DeleteSessionResponse{}, nil
	}
	if err := l.o().DeleteSession(ctx, req.SessionId); err != nil {
		return nil, err
	}
	return &localv1.DeleteSessionResponse{}, nil
}

func (l *localService) RenameSession(ctx context.Context, req *localv1.RenameSessionRequest) (*localv1.SessionResponse, error) {
	if m, ok, err := l.o().onlyMachine(); ok {
		if err != nil {
			return nil, err
		}
		return sessionResponse(m.Rename(ctx, req.SessionId, req.Title))
	}
	return sessionResponse(l.o().RenameSession(ctx, req.SessionId, req.Title))
}

func (l *localService) MarkSessionSeen(ctx context.Context, req *sessionv1.MarkSessionSeenRequest) (*localv1.SessionResponse, error) {
	if m, ok, err := l.o().onlyMachine(); ok {
		if err != nil {
			return nil, err
		}
		return sessionResponse(m.MarkSeen(ctx, req))
	}
	return sessionResponse(l.o().MarkSessionSeen(ctx, req))
}

func (l *localService) UnarchiveSession(ctx context.Context, req *localv1.UnarchiveSessionRequest) (*localv1.SessionResponse, error) {
	if m, ok, err := l.o().onlyMachine(); ok {
		if err != nil {
			return nil, err
		}
		return sessionResponse(m.Unarchive(ctx, req.SessionId))
	}
	return sessionResponse(l.o().UnarchiveSession(ctx, req.SessionId))
}

func (l *localService) RollbackSession(ctx context.Context, req *localv1.RollbackSessionRequest) (*localv1.SessionResponse, error) {
	if m, ok, err := l.o().onlyMachine(); ok {
		if err != nil {
			return nil, err
		}
		return sessionResponse(m.Rollback(ctx, req.SessionId, req.Turn))
	}
	return sessionResponse(l.o().RollbackSession(ctx, req.SessionId, req.Turn))
}

func sessionResponse(s *sessionv1.Session, err error) (*localv1.SessionResponse, error) {
	if err != nil {
		return nil, err
	}
	return &localv1.SessionResponse{Session: s}, nil
}

func (l *localService) GetDiff(ctx context.Context, req *localv1.GetDiffRequest) (*sessionv1.GetDiffResponse, error) {
	if m, ok, err := l.o().onlyMachine(); ok {
		if err != nil {
			return nil, err
		}
		return m.Diff(ctx, &sessionv1.GetDiffRequest{SessionId: req.SessionId, Turn: req.Turn})
	}
	return l.o().GetDiff(ctx, req.SessionId, req.Turn)
}

func (l *localService) GetSessionInsights(ctx context.Context, req *sessionv1.GetSessionInsightsRequest) (*sessionv1.SessionInsights, error) {
	if m, ok, err := l.o().onlyMachine(); ok {
		if err != nil {
			return nil, err
		}
		return m.Insights(ctx, req)
	}
	return l.o().GetSessionInsights(ctx, req.SessionId)
}

func (l *localService) PushSession(ctx context.Context, req *localv1.PushSessionRequest) (*sessionv1.PushSessionResponse, error) {
	if m, ok, err := l.o().onlyMachine(); ok {
		if err != nil {
			return nil, err
		}
		return m.Push(ctx, req.SessionId)
	}
	return l.o().PushSession(ctx, req.SessionId)
}
