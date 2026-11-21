package owner

import (
	"context"
	"errors"
	"slices"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	herv1 "github.com/samir2015dzh/aide/gen/go/aide/her/v1"
	identityv1 "github.com/samir2015dzh/aide/gen/go/aide/identity/v1"
	localv1 "github.com/samir2015dzh/aide/gen/go/aide/local/v1"
	machinev1 "github.com/samir2015dzh/aide/gen/go/aide/machine/v1"
	relayv1 "github.com/samir2015dzh/aide/gen/go/aide/relay/v1"
	"github.com/samir2015dzh/aide/internal/machine"
	"github.com/samir2015dzh/aide/pkg/identity"
)

// Her is no longer part of Aide. LocalService still names her calls (the
// protocol is not changed here), so an app from before gets an answer it
// knows: unimplemented, as a daemon older than the call would say.

var errNoHer = connect.NewError(connect.CodeUnimplemented, errors.New("Aide no longer has Her"))

func (l *localService) SetHer(context.Context, *localv1.SetHerRequest) (*localv1.SetHerResponse, error) {
	return nil, errNoHer
}

func (l *localService) SetHerCommandConnector(context.Context, *localv1.SetHerCommandConnectorRequest) (*localv1.SetHerCommandConnectorResponse, error) {
	return nil, errNoHer
}

func (l *localService) HerOpen(context.Context, *localv1.HerOpenRequest, *connect.ServerStream[herv1.OpenEvent]) error {
	return errNoHer
}

func (l *localService) HerCall(context.Context, *localv1.HerCallRequest) (*herv1.CallResponse, error) {
	return nil, errNoHer
}

func (l *localService) HerSend(context.Context, *localv1.HerSendRequest) (*localv1.HerSendResponse, error) {
	return nil, errNoHer
}

func (l *localService) ListHerWaiting(context.Context, *localv1.ListHerWaitingRequest) (*localv1.ListHerWaitingResponse, error) {
	return nil, errNoHer
}

func (l *localService) DeleteHerWaiting(context.Context, *localv1.DeleteHerWaitingRequest) (*localv1.DeleteHerWaitingResponse, error) {
	return nil, errNoHer
}

func (l *localService) GetHerConversation(context.Context, *localv1.GetHerConversationRequest) (*localv1.GetHerConversationResponse, error) {
	return nil, errNoHer
}

func (l *localService) HerUpload(context.Context, *localv1.HerUploadRequest) (*localv1.HerUploadResponse, error) {
	return nil, errNoHer
}

func (l *localService) HerDownload(context.Context, *localv1.HerDownloadRequest) (*localv1.HerDownloadResponse, error) {
	return nil, errNoHer
}

func (l *localService) GetHerConsole(context.Context, *localv1.GetHerConsoleRequest) (*localv1.GetHerConsoleResponse, error) {
	return nil, errNoHer
}

// The steps of the owner's store that made Her's tables. The store counts
// its steps, so they stay where they were: a store made before keeps its
// place, and a new one gets the same (empty) tables. Nothing reads or
// writes them any more.

var herMigrations = `
CREATE TABLE her_waiting (
  seq INTEGER PRIMARY KEY AUTOINCREMENT,
  id TEXT NOT NULL UNIQUE,
  machine_id TEXT NOT NULL,
  bot_id TEXT NOT NULL,
  waiting BLOB NOT NULL
);
CREATE TABLE her_status (
  machine_id TEXT PRIMARY KEY,
  status BLOB NOT NULL
);
`

var herKeptMigrations = `
CREATE TABLE her_bots (
  machine_id TEXT PRIMARY KEY,
  bot BLOB NOT NULL
);
CREATE TABLE her_conversations (
  machine_id TEXT PRIMARY KEY,
  snapshot BLOB NOT NULL
);
`

var herDeliveriesMigrations = `
CREATE TABLE her_deliveries (
  machine_id TEXT PRIMARY KEY,
  page BLOB NOT NULL
);
`

// olderMachine is a machine's answer to an admin command it doesn't know.
// Those up to 0.1.5 said so with invalid_argument; that answer is made
// unimplemented, as later ones give and the devices show as "update Aide
// there" (docs/version-skew.md §6.3). Any other error is as it came.
func olderMachine(err error) error {
	if connect.CodeOf(err) == connect.CodeInvalidArgument {
		var ce *connect.Error
		if errors.As(err, &ce) && ce.Message() == "unknown admin command" {
			return connect.NewError(connect.CodeUnimplemented, errors.New("Aide on this machine is older and doesn't know this command: update it there, then try again"))
		}
	}
	return err
}

// What a machine that ran Her once may still hold, and the owner's devices
// still clear away: her data there, erased when the owner removes the
// machine (or its device) and asks for it, and the grants she held, revoked
// with the machine.

// eraseHerData has a machine erase the data Her left there, signed by the
// root key as turning her off was: the machine answers how it went.
func (o *Owner) eraseHerData(ctx context.Context, machineID string) error {
	if _, ok, _ := o.onlyMachine(); ok {
		return connect.NewError(connect.CodeFailedPrecondition, errors.New("one of your devices has to erase Her's data: this computer is only a machine"))
	}
	signed, err := o.signRoot(ctx, &identityv1.AdminCommand{OwnerId: o.ownerID, CommandId: identity.RandomCode(16), TargetMachineId: machineID,
		ExpiresAt: timestamppb.New(time.Now().Add(5 * time.Minute)),
		Command:   &identityv1.AdminCommand_SetHer{SetHer: &identityv1.SetHer{Erase: true}}})
	if err != nil {
		return err
	}
	c, err := o.relay(ctx)
	if err != nil {
		return err
	}
	n, err := c.Machine(ctx, machineID)
	if err != nil {
		return connect.NewError(connect.CodeUnavailable, err)
	}
	res, err := n.Admin.ApplyAdminCommand(ctx, &machinev1.ApplyAdminCommandRequest{Command: signed[0]})
	if err != nil {
		c.ForgetMachine(n)
		return olderMachine(err)
	}
	return herErased(res.GetMessage())
}

// herErased reads a machine's answer to being asked to erase Her's data.
func herErased(msg string) error {
	switch msg {
	case machine.HerErased:
		return nil
	case machine.HerOff:
		return connect.NewError(connect.CodeFailedPrecondition, errors.New("Her is off, but her data stays: this machine's Aide is too old to erase it. Update it, then try again"))
	default:
		return connect.NewError(connect.CodeInternal, errors.New(msg))
	}
}

// herGrants are the ids of the grants Her held on a machine and that still
// stand: revoked with the machine. One that stands on a machine the owner
// keeps is theirs to revoke, as any agent's (RevokeClient).
func (o *Owner) herGrants(ctx context.Context, machineID string) []string {
	ctl, _, err := o.control(ctx)
	if err != nil {
		return nil
	}
	res, err := ctl.ListClients(ctx, &relayv1.ListClientsRequest{})
	if err != nil {
		return nil
	}
	var ids []string
	for _, c := range res.Clients {
		g := &identityv1.Grant{}
		if identity.Unverified(c.Grant, g) != nil || g.OwnerId != o.ownerID || g.HerMachineId != machineID || c.Revoked || slices.Contains(ids, g.GrantId) {
			continue
		}
		ids = append(ids, g.GrantId)
	}
	return ids
}

// forgetHer drops what this device kept of a removed machine's Her.
func (o *Owner) forgetHer(machineID string) {
	for _, table := range []string{"her_waiting", "her_status", "her_bots", "her_conversations", "her_deliveries"} {
		if _, err := o.st.db.Exec(`DELETE FROM `+table+` WHERE machine_id=?`, machineID); err != nil {
			o.log.Warn("forgetting a removed machine's Her", "machine", machineID, "table", table, "err", err)
		}
	}
}
