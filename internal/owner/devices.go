package owner

import (
	"bytes"
	"cmp"
	"context"
	"crypto/ecdsa"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	identityv1 "github.com/samir2015dzh/aide/gen/go/aide/identity/v1"
	localv1 "github.com/samir2015dzh/aide/gen/go/aide/local/v1"
	machinev1 "github.com/samir2015dzh/aide/gen/go/aide/machine/v1"
	relayv1 "github.com/samir2015dzh/aide/gen/go/aide/relay/v1"
	"github.com/samir2015dzh/aide/gen/go/aide/relay/v1/relayv1connect"
	"github.com/samir2015dzh/aide/internal/machine"
	"github.com/samir2015dzh/aide/pkg/identity"
)

// The owner's devices (docs/accounts.md §4): every device that logged in
// with the account and password, and those set up before accounts, whose
// root key the root cert lists. Any of them signs another out.

func hasKey(rc *identityv1.RootCert, spki []byte) bool {
	return slices.ContainsFunc(rc.GetRootKeys(), func(rk *identityv1.RootKey) bool { return bytes.Equal(rk.PublicKey, spki) })
}

// adoptChain keeps a longer root chain the relay handed over.
func (o *Owner) adoptChain(c *identityv1.RootChain) {
	o.mu.Lock()
	trust := o.trust
	o.mu.Unlock()
	if c == nil || trust == nil {
		return
	}
	changed, err := trust.AcceptRootChain(c)
	if err != nil {
		o.log.Warn("root chain from the relay refused", "err", err)
		return
	}
	if changed {
		b, _ := proto.Marshal(trust.RootChain())
		o.st.set(kRootChain, b)
	}
}

// latestChain brings the root chain up to date before changing it.
func (o *Owner) latestChain(ctx context.Context) (relayv1connect.OwnerControlServiceClient, error) {
	ctl, _, err := o.control(ctx)
	if err != nil {
		return nil, err
	}
	res, err := ctl.ListMachines(ctx, &relayv1.ListMachinesRequest{})
	if err != nil {
		return nil, err
	}
	o.adoptChain(res.RootChain)
	return ctl, nil
}

// ListDevices lists the owner's devices, with their own machines.
func (o *Owner) ListDevices(ctx context.Context) ([]*localv1.Device, error) {
	if err := o.requireInit(); err != nil {
		return nil, err
	}
	machines, err := o.machines(ctx) // the replica's poll brings the chain up to date too
	if err != nil {
		return nil, err
	}
	s, err := o.rep.use(ctx)
	if err != nil {
		return nil, err
	}
	o.rep.mu.Lock()
	entries, err := listed(&s.devices)
	o.rep.mu.Unlock()
	if err != nil {
		return nil, err
	}
	own := o.Status().DeviceKeyId
	var out []*localv1.Device
	for _, e := range entries {
		d := &localv1.Device{KeyId: e.KeyId, Label: e.Label, Platform: e.Platform, Legacy: e.Legacy, ThisDevice: e.KeyId == own}
		for _, m := range machines {
			if m.DeviceKeyId == d.KeyId {
				d.MachineId, d.MachineOnline = m.MachineId, m.Online
			}
		}
		out = append(out, d)
	}
	// This device first, then the others by name.
	slices.SortStableFunc(out, func(a, b *localv1.Device) int {
		if a.ThisDevice != b.ThisDevice {
			if a.ThisDevice {
				return -1
			}
			return 1
		}
		return cmp.Compare(a.Label, b.Label)
	})
	return out, nil
}

// relayDevices asks the server for the owner's devices.
func (o *Owner) relayDevices(ctx context.Context) ([]*relayv1.DeviceEntry, error) {
	ctl, _, err := o.control(ctx)
	if err != nil {
		return nil, err
	}
	res, err := ctl.ListDevices(ctx, &relayv1.ListDevicesRequest{})
	if err != nil {
		return nil, err
	}
	return res.Devices, nil
}

// RemoveDevice signs another device out: the server no longer admits it,
// and removes its own machine. With eraseHer, Her's data on that machine is
// erased first (docs/her-server.md §5.6): the machine must answer, or
// nothing is removed.
func (o *Owner) RemoveDevice(ctx context.Context, keyID string, eraseHer bool) (*localv1.RemoveDeviceResponse, error) {
	if err := o.requireInit(); err != nil {
		return nil, err
	}
	if keyID == o.Status().DeviceKeyId {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("sign this device out from another of your devices"))
	}
	ctl, err := o.latestChain(ctx)
	if err != nil {
		return nil, err
	}
	if eraseHer {
		machines, err := o.machines(ctx)
		if err != nil {
			return nil, err
		}
		var own string
		for _, m := range machines {
			if m.DeviceKeyId == keyID {
				own = m.MachineId
			}
		}
		if own == "" {
			return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("that device has no machine of its own, where Her's data would be"))
		}
		c, err := o.relay(ctx)
		if err != nil {
			return nil, err
		}
		n, err := c.Machine(ctx, own)
		if err != nil {
			return nil, connect.NewError(connect.CodeUnavailable, fmt.Errorf("its machine doesn't answer, so Her's data there can't be erased now: %w", err))
		}
		signed, err := o.signRoot(ctx, &identityv1.AdminCommand{OwnerId: o.ownerID, CommandId: identity.RandomCode(16), TargetMachineId: own,
			ExpiresAt: timestamppb.New(time.Now().Add(5 * time.Minute)),
			Command:   &identityv1.AdminCommand_SetHer{SetHer: &identityv1.SetHer{Erase: true}}})
		if err != nil {
			return nil, err
		}
		res, err := n.Admin.ApplyAdminCommand(ctx, &machinev1.ApplyAdminCommandRequest{Command: signed[0]})
		if err == nil {
			err = herErased(res.GetMessage())
		}
		if err != nil {
			return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("Her's data stays on its machine, so the device stays: %w", err))
		}
	}
	res, err := ctl.RemoveDevice(ctx, &relayv1.RemoveDeviceRequest{KeyId: keyID})
	if err != nil {
		return nil, err
	}
	o.latestChain(ctx)
	o.wrote(ctx, nil, true)
	return &localv1.RemoveDeviceResponse{ReissuedMachines: res.ReissuedMachines, ReissuedGrants: res.ReissuedGrants}, nil
}

// checkRemoved finds out, after the server refused this device (refusal),
// whether it is still one of the owner's: another may have signed it out,
// or the owner erased everything.
func (o *Owner) checkRemoved(refusal error) {
	o.mu.Lock()
	ownerID, relayURL, rootPub, trust := o.ownerID, o.relayURL, o.rootPub, o.trust
	o.mu.Unlock()
	if ownerID == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	info, err := o.publicClient(relayURL).GetInfo(ctx, &relayv1.GetInfoRequest{OwnerId: ownerID})
	if err != nil {
		return
	}
	removed := info.OwnerId != ownerID || refusal != nil && strings.Contains(refusal.Error(), "this device was signed out")
	if !removed {
		o.adoptChain(info.RootChain)
		removed = o.legacyRemoved(trust, rootPub)
	}
	o.mu.Lock()
	if o.ownerID == ownerID { // not left meanwhile
		o.removed = removed
	}
	erasing, onErase := o.erasing != nil, o.onErase
	o.mu.Unlock()
	// The owner erased everything from another device: this one erases
	// itself (the one that erased waits for FinishErase).
	if removed && !erasing && onErase != nil {
		if order := o.eraseOrder(info); order != nil {
			go onErase(o, order.EraseId, o.deviceName(info.EraseOrder))
		}
	}
}

// legacyRemoved: a device set up before accounts, whose root key the
// root cert no longer lists. (A device that logged in is told so when
// it connects: "this device was signed out".)
func (o *Owner) legacyRemoved(trust *identity.Trust, rootPub *ecdsa.PublicKey) bool {
	o.mu.Lock()
	dev := o.device
	o.mu.Unlock()
	if trust == nil || rootPub == nil || dev != nil && rootPub.Equal(dev.Public()) {
		return false
	}
	return !hasKey(trust.Root(), identity.MarshalPublicKey(rootPub))
}

// Leave forgets the owner on this device once it was signed out, so it can
// log in again. Its own machine, removed with it, is retired: it keeps its
// projects and comes back under a new id.
func (o *Owner) Leave(ctx context.Context) error {
	if !o.Initialized() && o.machineOnly() {
		return o.leaveAsMachine(ctx)
	}
	o.mu.Lock()
	removed := o.removed
	o.mu.Unlock()
	if !removed {
		return connect.NewError(connect.CodeFailedPrecondition, errors.New("this device is still one of the owner's; remove it from another device first"))
	}
	o.stopLocalMachine()
	if err := machine.Retire(o.localMachineConfig()); err != nil {
		return err
	}
	o.mu.Lock()
	if o.client != nil {
		o.client.Close()
		o.client = nil
	}
	o.ownerID, o.relayURL, o.rootPub, o.trust, o.device, o.deviceCert, o.removed = "", "", nil, nil, nil, nil, false
	o.mcpURL, o.mcpOld = "", false
	o.signedOut, o.account = false, ""
	o.mu.Unlock()
	for _, k := range []string{kOwnerID, kSignedOut, kAccount, kDirectAddrs} {
		if err := o.st.set(k, []byte{}); err != nil {
			return err
		}
	}
	if err := o.st.clearGrants(); err != nil {
		return err
	}
	return nil
}

// SignOut stops using this profile's keys without giving them up: it stops
// its notifications, and Run (restarted by the device) neither connects nor
// starts its machine until SignIn.
func (o *Owner) SignOut(ctx context.Context) error {
	if err := o.requireInit(); err != nil {
		return err
	}
	pctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	if err := o.RegisterPush(pctx, &localv1.RegisterPushRequest{}); err != nil {
		o.log.Debug("signing out: notifications not stopped", "err", err)
	}
	cancel()
	if err := o.st.set(kSignedOut, []byte("1")); err != nil {
		return err
	}
	o.mu.Lock()
	o.signedOut = true
	o.mu.Unlock()
	return nil
}

// SignIn takes this profile's keys up again. If the owner meanwhile removed
// this device, or started over, it signs in removed, for Leave.
func (o *Owner) SignIn(ctx context.Context, req *localv1.SignInRequest) error {
	if !o.Initialized() {
		return connect.NewError(connect.CodeFailedPrecondition, errors.New("Aide is not set up yet"))
	}
	if !o.isSignedOut() {
		return nil
	}
	o.mu.Lock()
	ownerID, relayURL, rootPub, trust := o.ownerID, o.relayURL, o.rootPub, o.trust
	o.mu.Unlock()
	info, err := o.publicClient(relayURL).GetInfo(ctx, &relayv1.GetInfoRequest{OwnerId: ownerID})
	if err != nil {
		return connect.NewError(connect.CodeUnavailable, fmt.Errorf("relay unreachable: %w", err))
	}
	removed := info.OwnerId != ownerID
	if !removed {
		o.adoptChain(info.RootChain)
		removed = o.legacyRemoved(trust, rootPub)
	}
	if err := o.st.set(kSignedOut, []byte{}); err != nil {
		return err
	}
	if req.Account != "" {
		if err := o.st.set(kAccount, []byte(req.Account)); err != nil {
			return err
		}
	}
	o.mu.Lock()
	o.signedOut, o.removed = false, removed
	if req.Account != "" {
		o.account = req.Account
	}
	o.mu.Unlock()
	return nil
}
