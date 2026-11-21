// Package owner implements the owner role of the aided bundled with
// Aide.app, on each of the owner's trusted devices: onboarding, devices
// joining and leaving, pairing approval, grants, revocation, machine
// administration and cross-machine session views. Anything that adds
// permissions is signed by this device's root key, held by the app.
package owner

import (
	"context"
	"crypto/ecdsa"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	identityv1 "github.com/samir2015dzh/aide/gen/go/aide/identity/v1"
	localv1 "github.com/samir2015dzh/aide/gen/go/aide/local/v1"
	machinev1 "github.com/samir2015dzh/aide/gen/go/aide/machine/v1"
	relayv1 "github.com/samir2015dzh/aide/gen/go/aide/relay/v1"
	"github.com/samir2015dzh/aide/gen/go/aide/relay/v1/relayv1connect"
	sessionv1 "github.com/samir2015dzh/aide/gen/go/aide/session/v1"
	"github.com/samir2015dzh/aide/internal/flock"
	"github.com/samir2015dzh/aide/internal/machine"
	"github.com/samir2015dzh/aide/internal/peer"
	"github.com/samir2015dzh/aide/internal/sessionstate"
	"github.com/samir2015dzh/aide/pkg/identity"
	"github.com/samir2015dzh/aide/pkg/transport"
)

type Config struct {
	DataDir    string
	Logger     *slog.Logger
	HTTPClient *http.Client
	// MachineConfig is used when this Mac is enabled as an execution machine.
	MachineConfig machine.Config
	// NoLocalMachine: this device can never be an execution machine (an
	// iPhone), so requests to make it one are refused.
	NoLocalMachine bool
}

// errNoLocalMachine refuses making a device that cannot run agents a machine.
var errNoLocalMachine = connect.NewError(connect.CodeFailedPrecondition, errors.New("this device cannot be an execution machine"))

type Owner struct {
	cfg Config
	log *slog.Logger
	st  *store
	// lock holds the profile's directory for this process.
	lock *flock.Lock
	// rep is this device's copy of the owner's world, which the List
	// calls answer from (replica.go).
	rep *replica
	// limits: what sessions' states are worked out with (docs/session-states.md).
	limits sessionstate.Limits

	mu         sync.Mutex
	ownerID    string
	relayURL   string
	rootPub    *ecdsa.PublicKey
	trust      *identity.Trust
	device     *identity.SoftwareKey
	deviceCert *identityv1.Signed
	client     *peer.Client
	dialing    *relayDial   // the dial to the relay under way, if any
	kept       *peer.Client // client, kept over a pause: not known to work yet
	mcpURL     string       // the relay's own MCP address, with its key (GetMCP)
	mcpOld     bool         // the relay is older than GetMCP
	pendingMu  sync.Mutex   // pending names (pendingNames)
	cardsMu    sync.Mutex   // one card sync at a time (cards.go)
	removed    bool         // the relay no longer counts this device as the owner's
	signedOut  bool         // SignOut: set up, but it neither connects nor signs
	account    string       // the account, as the center named it (display)

	localMachine *machine.Machine
	// lastMachines: the machines as the relay last listed them, shown
	// offline while the relay cannot be reached.
	lastMachines []*localv1.Machine
	// lastClients: the agents as the relay last listed them, shown while it
	// cannot be reached.
	lastClients []*localv1.Client
	// seen: the ids of the other machines the relay last listed online,
	// comma-separated (kSeenOnline).
	seen        string
	localCancel context.CancelFunc
	localDone   chan struct{} // closed once the local machine stopped and closed
	runCtx      context.Context

	// erasing: the erase this device started, until FinishErase (erase.go).
	erasing *eraseRun
	// erased: what the erase that emptied this profile did, for its first
	// run to say (kErased).
	erased *localv1.Erased
	// onErase: the device erasing this profile, whose owner erased
	// everything from another device (set by Device).
	// by: the trusted device that erased, by name.
	onErase func(o *Owner, eraseID, by string)
	// wiping: the profile is being erased (once, however many refusals
	// asked).
	wiping bool

	// signHook, when set, is how tests have something signed (signRoot).
	signHook func(ctx context.Context, msgs []proto.Message) ([]*identityv1.Signed, error)

	directMu   sync.Mutex
	direct     map[string][]string // machineID -> normalized direct_addrs, persisted under kDirectAddrs
	lastDirect map[string]string   // machineID -> last winning direct address, tried first on reconnect

	// dshSeen: each machine's DSH sessions as ListDSHSessions last read
	// them, listed as lost while it can't be reached (in memory only).
	dshMu   sync.Mutex
	dshSeen map[string]dshSeen
	// claudeSeen: the same for the Claude desktop app's sessions.
	claudeSeen map[string]claudeSeen
}

// claudeSeen is a machine's Claude sessions, and when they were read.
type claudeSeen struct {
	at      time.Time
	entries []*localv1.ClaudeSessionEntry
}

// dshSeen is a machine's DSH sessions, and when they were read.
type dshSeen struct {
	at      time.Time
	entries []*localv1.DSHSessionEntry
}

const (
	kOwnerID      = "owner_id"
	kRelayURL     = "relay_url"
	kRootPub      = "root_public_key"
	kRootChain    = "root_chain"
	kDeviceCert   = "device_cert"
	kLocalMachine = "local_machine"
	kSignedOut    = "signed_out"
	kAccount      = "account"
	kSeenOnline   = "seen_online"
	// kLastMachines and kLastClients: what the relay last listed, so that a
	// start without the relay still shows them (keepListed, listClients).
	kLastMachines = "last_machines"
	kLastClients  = "last_clients"
	// kMachineDir: where this profile's machine is, when not in its own
	// directory (a device's machine from before profiles, profiles.go).
	kMachineDir = "machine_dir"
	// kDirectAddrs caches each machine's direct LAN/IPv6 E2E tunnel endpoints
	// across app restarts so direct racing can start before relay connects.
	kDirectAddrs = "direct_addrs"
)

// openProfile opens one profile (profiles.go): cfg.DataDir is its
// directory.
func openProfile(cfg Config) (*Owner, error) {
	lock, err := flock.Acquire(cfg.DataDir)
	if err != nil {
		return nil, err
	}
	st, err := openStore(filepath.Join(cfg.DataDir, "owner.db"))
	if err != nil {
		lock.Release()
		return nil, err
	}
	o := &Owner{cfg: cfg, log: cfg.Logger, st: st, lock: lock, limits: sessionstate.FromEnv()}
	o.rep = newReplica(o)
	return o, o.load()
}

func (o *Owner) load() error {
	account, _ := o.st.get(kAccount)
	o.account = string(account)
	seen, _ := o.st.get(kSeenOnline)
	o.seen = string(seen)
	if b, _ := o.st.get(kLastMachines); len(b) > 0 {
		var kept localv1.ListMachinesResponse
		if proto.Unmarshal(b, &kept) == nil {
			o.lastMachines = kept.Machines
		}
	}
	if b, _ := o.st.get(kLastClients); len(b) > 0 {
		var kept localv1.ListClientsResponse
		if proto.Unmarshal(b, &kept) == nil {
			o.lastClients = kept.Clients
		}
	}
	if b, _ := o.st.get(kDirectAddrs); len(b) > 0 {
		var direct map[string][]string
		if json.Unmarshal(b, &direct) == nil {
			o.direct = direct
		}
	}
	if b, _ := o.st.get(kErased); len(b) > 0 {
		var e localv1.Erased
		if proto.Unmarshal(b, &e) == nil {
			o.erased = &e
		}
	}
	id, _ := o.st.get(kOwnerID)
	if len(id) == 0 {
		return nil
	}
	signedOut, _ := o.st.get(kSignedOut)
	o.signedOut = string(signedOut) == "1"
	relay, _ := o.st.get(kRelayURL)
	rootPub, _ := o.st.get(kRootPub)
	pub, err := identity.ParsePublicKey(rootPub)
	if err != nil {
		return err
	}
	trust := identity.NewTrust(string(id))
	rc := &identityv1.RootChain{}
	if b, err := o.st.get(kRootChain); err != nil || b == nil || proto.Unmarshal(b, rc) != nil {
		return fmt.Errorf("owner root chain missing: %v", err)
	}
	if _, err := trust.AcceptRootChain(rc); err != nil {
		return err
	}
	dev, err := identity.LoadKey(filepath.Join(o.cfg.DataDir, "keys", "device.key"))
	if err != nil {
		return err
	}
	dc, err := o.st.getSigned(kDeviceCert)
	if err != nil {
		return err
	}
	o.ownerID, o.relayURL, o.rootPub, o.trust, o.device, o.deviceCert = string(id), string(relay), pub, trust, dev, dc
	return nil
}

func (o *Owner) Initialized() bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.ownerID != ""
}

// Run keeps background work going: the local execution machine (if
// enabled) and the owner relay connection. A profile that is signed out
// does neither.
func (o *Owner) Run(ctx context.Context) {
	o.mu.Lock()
	o.runCtx = ctx
	signedOut := o.signedOut
	o.mu.Unlock()
	if signedOut {
		<-ctx.Done()
		return
	}
	if v, _ := o.st.get(kLocalMachine); (string(v) == "1" && o.Initialized()) || o.machineOnly() {
		if err := o.startLocalMachine(); err != nil {
			o.log.Warn("local machine did not start", "err", err)
		}
	}
	go o.namePending(ctx)
	o.stayConnected(ctx)
	o.mu.Lock()
	if o.client != nil && o.client != o.kept {
		o.client.Close()
		o.client = nil
	}
	o.mu.Unlock()
	o.stopLocalMachine()
}

// stayConnected keeps the relay connection up once set up, so the relay
// sees the owner online and pairing requests reach it without waiting for
// the next call.
func (o *Owner) stayConnected(ctx context.Context) {
	backoff := time.Second
	warned := false
	for ctx.Err() == nil {
		var done <-chan struct{}
		var c *peer.Client
		if o.Initialized() {
			started := time.Now()
			var err error
			if c, err = o.relay(ctx); err == nil {
				done = c.Done()
				warned = false
				o.log.Info("owner: relay connected", "after", time.Since(started).Round(time.Millisecond))
				o.kickScreens()
			} else if !warned && ctx.Err() == nil {
				warned = true
				o.log.Warn("owner: relay unreachable", "err", err, "retry_in", backoff)
			} else {
				o.log.Debug("relay unreachable", "err", err, "retry_in", backoff)
			}
		}
		if done != nil {
			select {
			case <-ctx.Done():
				continue
			case <-done:
			}
			o.log.Info("owner: relay disconnected")
			o.kickScreens()
			err := c.Refused()
			if err == nil {
				backoff = time.Second
				continue
			}
			go o.checkRemoved(err) // refused after admission was pipelined: wait as for a failed dial
		}
		select {
		case <-ctx.Done():
		case <-time.After(backoff):
		}
		if o.Initialized() {
			backoff = min(backoff*2, 10*time.Second)
		}
	}
}

// kickScreens has the screens look again now (the status changed).
func (o *Owner) kickScreens() {
	o.rep.mu.Lock()
	o.rep.kickLocked()
	o.rep.mu.Unlock()
}

func (o *Owner) requireInit() error {
	if !o.Initialized() {
		return connect.NewError(connect.CodeFailedPrecondition, errors.New("Aide is not set up yet"))
	}
	if o.isSignedOut() {
		return errSignedOut
	}
	return nil
}

var errSignedOut = connect.NewError(connect.CodeFailedPrecondition, errors.New("this account is signed out on this device; sign in again"))

func (o *Owner) isSignedOut() bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.signedOut
}

func (o *Owner) credentials() *identityv1.Credentials {
	o.mu.Lock()
	defer o.mu.Unlock()
	return &identityv1.Credentials{RootChain: o.trust.RootChain(), Subject: &identityv1.Credentials_DeviceCert{DeviceCert: o.deviceCert}}
}

// relayDial is a dial to the relay under way; err is set before done
// closes. again: it ended because its caller left, so a caller that waited
// dials itself.
type relayDial struct {
	done  chan struct{}
	err   error
	again bool
}

// relay returns the owner's relay connection, (re)connecting as needed. It
// dials without o.mu, so that what only reads (the status, the lists the
// replica holds) never waits on the network; a caller that comes while a
// dial is under way waits for that one. Admission is pipelined: the first
// calls, and end-to-end handshakes with the machines last seen online, go
// out with it (stayConnected notices a refusal). A connection kept over a
// pause is tried meanwhile (connectOrKeep).
func (o *Owner) relay(ctx context.Context) (*peer.Client, error) {
	if err := o.requireInit(); err != nil {
		return nil, err
	}
	for {
		o.mu.Lock()
		var kept *peer.Client
		if c := o.client; c != nil {
			select {
			case <-c.Done():
				o.client = nil
			default:
				if c != o.kept {
					o.mu.Unlock()
					return c, nil
				}
				kept = c
			}
		}
		if w := o.dialing; w != nil {
			o.mu.Unlock()
			select {
			case <-w.done:
				if w.err != nil && !w.again {
					return nil, w.err
				}
				continue
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		d := o.dialerLocked()
		if d == nil {
			o.mu.Unlock()
			return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("no relay configured"))
		}
		w := &relayDial{done: make(chan struct{})}
		o.dialing = w
		o.mu.Unlock()
		var adoptPrewarm func(*peer.Client)
		if kept == nil {
			adoptPrewarm = prewarmDirect(ctx, d, o.seenOnline())
		}
		c, err := connectOrKeep(ctx, d, kept)
		if adoptPrewarm != nil {
			if err == nil {
				adoptPrewarm(c)
			} else {
				adoptPrewarm(nil)
			}
		}
		o.mu.Lock()
		o.dialing = nil
		if err == nil {
			o.client = c
		}
		if err == nil || ctx.Err() == nil {
			o.kept = nil
		}
		o.mu.Unlock()
		if err != nil {
			if errors.Is(err, transport.ErrAdmission) {
				go o.checkRemoved(err)
			}
			w.err, w.again = connect.NewError(connect.CodeUnavailable, fmt.Errorf("relay unreachable: %w", err)), ctx.Err() != nil
			close(w.done)
			return nil, w.err
		}
		c.Warm(o.seenOnline()) // before the callers waiting wake, so that they join these handshakes
		close(w.done)
		if c == kept {
			return c, nil
		}
		go o.fetchMCP()
		go o.refreshCards()
		go func() { // admitted: not removed (with admission pipelined, only once the relay said so)
			actx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if c.Admitted(actx) == nil {
				o.mu.Lock()
				if o.client == c {
					o.removed = false
				}
				o.mu.Unlock()
			}
		}()
		return c, nil
	}
}

// connectOrKeep dials the relay. With kept, the connection kept over a
// pause, it pings that one meanwhile and keeps it if it answers first:
// after a short pause it mostly still works, and keeping it saves the
// dial's round trips and the machines' handshakes (their connections
// through it are kept too). After a long one it may be dead without a
// word, and the dial is no later for it. The one not kept is closed.
func connectOrKeep(ctx context.Context, d *peer.Dialer, kept *peer.Client) (*peer.Client, error) {
	if kept == nil {
		return d.Connect(ctx)
	}
	dctx, cancel := context.WithCancel(ctx)
	defer cancel()
	type dial struct {
		c   *peer.Client
		err error
	}
	dialed, pinged := make(chan dial, 1), make(chan error, 1)
	go func() {
		c, err := d.Connect(dctx)
		dialed <- dial{c, err}
	}()
	go func() { pinged <- kept.Ping(dctx) }()
	var err error
	for dialing, pinging := true, true; dialing || pinging; {
		select {
		case perr := <-pinged:
			pinging = false
			if perr == nil {
				if dialing {
					go func() {
						if r := <-dialed; r.c != nil {
							r.c.Close()
						}
					}()
				}
				return kept, nil
			}
		case r := <-dialed:
			dialing = false
			if r.err == nil {
				kept.Close()
				return r.c, nil
			}
			err = r.err
		}
	}
	if ctx.Err() == nil { // else its caller left: it is tried at the next call
		kept.Close()
	}
	return nil, err
}

type prewarmHit struct {
	id string
	n  *peer.Machine
}

// prewarmDirect races direct E2E dials for seen online machines while a cold
// relay connection is being established, and returns an adopt function that
// cancels remaining attempts and installs any completed tunnels onto c.
func prewarmDirect(ctx context.Context, d *peer.Dialer, ids []string) func(*peer.Client) {
	if d == nil || d.Direct == nil || len(ids) == 0 {
		return nil
	}
	targets := make(map[string][]string, len(ids))
	for _, id := range ids {
		if addrs := d.Direct(id); len(addrs) > 0 {
			targets[id] = addrs
		}
	}
	if len(targets) == 0 {
		return nil
	}
	preCtx, cancel := context.WithCancel(ctx)
	hits := make(chan prewarmHit, len(targets))
	var wg sync.WaitGroup
	for id, addrs := range targets {
		wg.Add(1)
		go func(id string, addrs []string) {
			defer wg.Done()
			if n, err := d.DialDirect(preCtx, id, addrs); err == nil {
				hits <- prewarmHit{id: id, n: n}
			}
		}(id, addrs)
	}
	return func(c *peer.Client) {
		cancel()
		for {
			select {
			case h := <-hits:
				if c == nil {
					h.n.Close()
				} else {
					c.AdoptDirect(h.id, h.n)
				}
			default:
				go func() {
					wg.Wait()
					close(hits)
					for h := range hits {
						h.n.Close()
					}
				}()
				return
			}
		}
	}
}

// keepRelay keeps the relay connection over a pause, to be tried again
// before it is dialed again (connectOrKeep).
func (o *Owner) keepRelay() {
	o.mu.Lock()
	o.kept = o.client
	o.mu.Unlock()
}

// dropRelay closes the relay connection, so the next call dials a new one:
// after the network changed, the old one may be dead without anything
// having noticed yet.
func (o *Owner) dropRelay() {
	o.mu.Lock()
	c := o.client
	o.client = nil
	o.mu.Unlock()
	if c != nil {
		c.Close()
	}
}

// reconnect verifies whether the active relay connection still answers Ping
// before dropping it and restarting the replica. Transient network events (e.g.
// iOS path changes) can fire without disrupting an active socket.
func (o *Owner) reconnect() {
	o.mu.Lock()
	c := o.client
	rctx := o.runCtx
	rep := o.rep
	if c == nil {
		o.mu.Unlock()
		if rep != nil {
			rep.restart()
		}
		return
	}
	o.mu.Unlock()
	if rctx == nil {
		rctx = context.Background()
	}
	go func() {
		pctx, cancel := context.WithTimeout(rctx, 500*time.Millisecond)
		err := c.Ping(pctx)
		cancel()
		if err == nil {
			c.ForgetDirect()
			return
		}
		o.mu.Lock()
		still := o.client == c
		if still {
			o.client = nil
		}
		if o.kept == c {
			o.kept = nil
		}
		o.mu.Unlock()
		c.Close()
		if still && rctx.Err() == nil && rep != nil {
			rep.restart()
		}
	}()
}

// fetchMCP learns the relay's MCP address, which it tells only the owner's
// devices (for Status).
func (o *Owner) fetchMCP() {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ctl, _, err := o.control(ctx)
	if err != nil {
		return
	}
	url, old, ok := mcpAnswer(ctl.GetMCP(ctx, &relayv1.GetMCPRequest{}))
	if !ok {
		return
	}
	o.mu.Lock()
	o.mcpURL, o.mcpOld = url, old
	o.mu.Unlock()
}

// mcpAnswer is what GetMCP's answer says: the relay's MCP address ("" when
// its MCP is off), or that the relay is older than GetMCP (it does not know
// the call); not ok when it did not answer (asked again at the next
// connection).
func mcpAnswer(res *relayv1.GetMCPResponse, err error) (url string, older, ok bool) {
	switch {
	case err == nil:
		return res.Url, false, true
	case connect.CodeOf(err) == connect.CodeUnimplemented:
		return "", true, true
	}
	return "", false, false
}

func (o *Owner) control(ctx context.Context) (relayv1connect.OwnerControlServiceClient, *peer.Client, error) {
	c, err := o.relay(ctx)
	if err != nil {
		return nil, nil, err
	}
	hc, err := c.Control()
	if err != nil {
		return nil, nil, err
	}
	return relayv1connect.NewOwnerControlServiceClient(hc, "http://relay", transport.ClientNoCompression), c, nil
}

// ---------------------------------------------------------------------------
// Onboarding.

func (o *Owner) Status() *localv1.GetStatusResponse {
	st := o.ownerStatus()
	if !st.Initialized && o.machineOnly() {
		o.machineOnlyStatus(st)
	}
	st.StateLimits = o.stateLimits()
	return st
}

func (o *Owner) ownerStatus() *localv1.GetStatusResponse {
	o.mu.Lock()
	defer o.mu.Unlock()
	st := &localv1.GetStatusResponse{Initialized: o.ownerID != "", OwnerId: o.ownerID, RelayUrl: o.relayURL, Version: machine.Version,
		Abilities: []string{"her_reply"}}
	if o.client != nil {
		select {
		case <-o.client.Done():
		default:
			st.RelayConnected = true
		}
	}
	if o.localMachine != nil {
		st.LocalMachineEnabled, st.LocalMachineId, st.LocalMachineName = true, o.localMachine.MachineID(), o.localMachine.Name()
	}
	if o.ownerID != "" {
		st.McpUrl, st.RelayOlderThanMcp = o.mcpURL, o.mcpOld
	}
	if o.rootPub != nil && o.ownerID != "" {
		st.DeviceKeyId = identity.KeyID(o.rootPub)
	}
	st.Removed, st.SignedOut, st.Account = o.removed, o.signedOut, o.account
	if o.ownerID == "" {
		st.Erased = o.erased
	}
	return st
}

// checkRelay makes sure this network reaches the relay, by HTTPS and by
// WebSocket, before logging in: a company network or proxy
// that blocks it fails here, with nothing created or signed.
func (o *Owner) checkRelay(ctx context.Context, relayURL string) error {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	_, err := o.publicClient(relayURL).GetInfo(ctx, &relayv1.GetInfoRequest{})
	if err == nil {
		err = transport.Probe(ctx, relayURL, o.cfg.HTTPClient)
	}
	if err == nil {
		return nil
	}
	host := strings.TrimPrefix(strings.TrimPrefix(relayURL, "https://"), "http://")
	var ce *connect.Error
	if errors.As(err, &ce) {
		err = errors.New(ce.Message()) // "403 Forbidden", from whatever answered instead of the relay
	}
	// The apps recognise this form and say it in the person's language.
	return connect.NewError(connect.CodeUnavailable, fmt.Errorf("relay unreachable (%s): %w", host, err))
}

func (o *Owner) publicClient(relayURL string) relayv1connect.RelayPublicServiceClient {
	return relayv1connect.NewRelayPublicServiceClient(o.cfg.HTTPClient, strings.TrimSuffix(relayURL, "/"))
}

func (o *Owner) saveIdentity(ownerID, relayURL string, rootPub []byte, chain *identityv1.RootChain, device *identity.SoftwareKey, deviceCert *identityv1.Signed, account string) error {
	trust := identity.NewTrust(ownerID)
	if _, err := trust.AcceptRootChain(chain); err != nil {
		return err
	}
	if _, _, err := trust.VerifyDeviceCert(deviceCert); err != nil {
		return err
	}
	if err := identity.SaveKey(filepath.Join(o.cfg.DataDir, "keys", "device.key"), device); err != nil {
		return err
	}
	b, _ := proto.Marshal(chain)
	for k, v := range map[string][]byte{kOwnerID: []byte(ownerID), kRelayURL: []byte(relayURL), kRootPub: rootPub, kRootChain: b, kAccount: []byte(account), kSignedOut: {}} {
		if err := o.st.set(k, v); err != nil {
			return err
		}
	}
	o.st.setSigned(kDeviceCert, deviceCert)
	pub, _ := identity.ParsePublicKey(rootPub)
	o.mu.Lock()
	if o.client != nil {
		o.client.Close()
		o.client = nil
	}
	o.ownerID, o.relayURL, o.rootPub, o.trust, o.device, o.deviceCert = ownerID, relayURL, pub, trust, device, deviceCert
	o.account, o.signedOut = account, false
	o.mu.Unlock()
	return nil
}

func (o *Owner) dialerLocked() *peer.Dialer {
	if o.ownerID == "" || o.device == nil || o.trust == nil {
		return nil
	}
	return &peer.Dialer{
		RelayURL:        o.relayURL,
		Role:            relayv1.Role_ROLE_OWNER,
		Key:             o.device,
		Trust:           o.trust,
		HTTPClient:      o.cfg.HTTPClient,
		Pipeline:        true,
		Credentials:     o.credentials,
		Direct:          o.directAddrs,
		OnDirectSuccess: o.noteDirectSuccess,
	}
}

func (o *Owner) dialer() *peer.Dialer {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.dialerLocked()
}

func (o *Owner) directAddrs(machineID string) []string {
	o.directMu.Lock()
	defer o.directMu.Unlock()
	out := slices.Clone(o.direct[machineID])
	if last := o.lastDirect[machineID]; last != "" {
		if idx := slices.Index(out, last); idx > 0 {
			out[0], out[idx] = out[idx], out[0]
		}
	}
	return out
}

func (o *Owner) noteDirectSuccess(machineID, addr string) {
	if addr == "" {
		return
	}
	o.directMu.Lock()
	if o.lastDirect == nil {
		o.lastDirect = map[string]string{}
	}
	o.lastDirect[machineID] = addr
	o.directMu.Unlock()
}

func (o *Owner) saveDirectAddrs(machineID string, addrs []string) {
	var clean []string
	for _, a := range addrs {
		if a = strings.TrimSpace(a); a != "" {
			clean = append(clean, a)
		}
	}
	o.directMu.Lock()
	defer o.directMu.Unlock()
	if slices.Equal(o.direct[machineID], clean) {
		return
	}
	if o.direct == nil {
		o.direct = map[string][]string{}
	}
	if len(clean) == 0 {
		delete(o.direct, machineID)
	} else {
		o.direct[machineID] = clean
	}
	if b, err := json.Marshal(o.direct); err == nil && o.st != nil {
		_ = o.st.set(kDirectAddrs, b)
	}
}

func normalizeRelayURL(s string) string {
	s = strings.TrimSuffix(strings.TrimSpace(s), "/")
	if !strings.Contains(s, "://") {
		// A relay reached by its IP address gets no certificate (it gets them
		// for a domain name): plain HTTP, which the apps warn about.
		host := s
		if h, _, err := net.SplitHostPort(s); err == nil {
			host = h
		}
		if net.ParseIP(strings.Trim(host, "[]")) != nil {
			return "http://" + s
		}
		s = "https://" + s
	}
	return s
}

// ---------------------------------------------------------------------------
// Local execution machine.

func (o *Owner) SetLocalMachine(ctx context.Context, enabled bool, name string) (string, error) {
	if err := o.requireInit(); err != nil {
		return "", err
	}
	if enabled && o.cfg.NoLocalMachine {
		return "", errNoLocalMachine
	}
	if !enabled {
		o.mu.Lock()
		if o.localCancel != nil {
			o.localCancel()
			o.localCancel, o.localMachine = nil, nil
		}
		o.mu.Unlock()
		return "", o.st.set(kLocalMachine, []byte("0"))
	}
	o.mu.Lock()
	running := o.localMachine
	o.mu.Unlock()
	if running != nil {
		return running.MachineID(), o.st.set(kLocalMachine, []byte("1"))
	}
	n, err := o.openLocalMachine()
	if err != nil {
		return "", err
	}
	paired := n.Paired() && n.OwnerID() == o.ownerID
	n.Close()
	if paired {
		if err := o.st.set(kLocalMachine, []byte("1")); err != nil {
			return "", err
		}
		if err := o.startLocalMachine(); err != nil {
			return "", err
		}
		return o.Status().LocalMachineId, nil
	}
	if name == "" {
		name, _ = os.Hostname()
	}
	nc, err := o.localMachineCert(o.ownerID, o.rootPub)
	if err != nil {
		return "", err
	}
	signed, err := o.signRoot(ctx, nc)
	if err != nil {
		return "", err
	}
	if err := o.installLocalMachine(ctx, signed[0], name); err != nil {
		return "", err
	}
	return o.Status().LocalMachineId, nil
}

// localMachineCert certifies this device's own machine, for this device's
// root key to sign: removing the device removes the machine too.
func (o *Owner) localMachineCert(ownerID string, rootPub *ecdsa.PublicKey) (*identityv1.MachineCert, error) {
	n, err := o.openLocalMachine()
	if err != nil {
		return nil, err
	}
	defer n.Close()
	return &identityv1.MachineCert{OwnerId: ownerID, MachineId: n.MachineID(), MachinePublicKey: n.PublicKeySPKI(),
		IssuedAt: timestamppb.Now(), DeviceKeyId: identity.KeyID(rootPub)}, nil
}

// installLocalMachine publishes this device's signed machine cert, installs
// it, names the machine and starts it.
func (o *Owner) installLocalMachine(ctx context.Context, cert *identityv1.Signed, name string) error {
	ctl, _, err := o.control(ctx)
	if err != nil {
		return err
	}
	if _, err := ctl.PublishMachineCert(ctx, &relayv1.PublishMachineCertRequest{MachineCert: cert}); err != nil {
		return err
	}
	n, err := o.openLocalMachine()
	if err != nil {
		return err
	}
	err = n.Install(o.ownerID, o.relayURL, o.trust.RootChain(), cert)
	if err == nil && name != "" {
		err = n.SetName(name)
	}
	n.Close()
	if err != nil {
		return err
	}
	if err := o.st.set(kLocalMachine, []byte("1")); err != nil {
		return err
	}
	return o.startLocalMachine()
}

func (o *Owner) openLocalMachine() (*machine.Machine, error) {
	return machine.Open(o.localMachineConfig())
}

func (o *Owner) localMachineConfig() machine.Config {
	cfg := o.cfg.MachineConfig
	cfg.DataDir = filepath.Join(o.cfg.DataDir, "machine")
	if dir, _ := o.st.get(kMachineDir); len(dir) > 0 {
		cfg.DataDir = string(dir)
	}
	if cfg.Logger == nil {
		cfg.Logger = o.log
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = o.cfg.HTTPClient
	}
	// A device that is only a machine has no owner of its own to hear an
	// erase on: its machine does (erase.go).
	cfg.SelfErase = !o.Initialized() && o.machineOnly()
	return cfg
}

func (o *Owner) startLocalMachine() error {
	o.mu.Lock()
	if o.localMachine != nil {
		o.mu.Unlock()
		return nil
	}
	parent := o.runCtx
	o.mu.Unlock()
	if parent == nil {
		parent = context.Background()
	}
	n, err := o.openLocalMachine()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(parent)
	done := make(chan struct{})
	o.mu.Lock()
	o.localMachine, o.localCancel, o.localDone = n, cancel, done
	o.mu.Unlock()
	go func() {
		err := n.Run(ctx)
		if err != nil && !errors.Is(err, machine.ErrErased) {
			o.log.Warn("local machine stopped", "err", err)
		}
		n.Close()
		close(done)
		if errors.Is(err, machine.ErrErased) && o.onErase != nil {
			o.mu.Lock()
			o.localCancel, o.localMachine, o.localDone = nil, nil, nil
			o.mu.Unlock()
			o.onErase(o, "", "")
		}
	}()
	return nil
}

// stopLocalMachine stops the local machine and waits until it is closed.
func (o *Owner) stopLocalMachine() {
	o.mu.Lock()
	cancel, done := o.localCancel, o.localDone
	o.localCancel, o.localMachine, o.localDone = nil, nil, nil
	o.mu.Unlock()
	if cancel == nil {
		return
	}
	cancel()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		o.log.Warn("local machine did not stop in time")
	}
}

// ---------------------------------------------------------------------------
// Pairing.

// SanitizeMachineName turns s into a machine name (see machine.SanitizeName).
func SanitizeMachineName(s string) string { return machine.SanitizeName(s) }

// renameTo is the name a rename gives: refused when nothing of s is left,
// rather than turned into "machine" as a pairing's would be.
func renameTo(s string) (string, error) {
	name := machine.CleanName(s)
	if name == "" {
		return "", connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("%q has no letter or digit; a machine name uses lowercase letters, digits and -", strings.TrimSpace(s)))
	}
	return name, nil
}

// kPendingNames: machine id → a name chosen at approval, to set on the
// machine once it connects (JSON).
const kPendingNames = "pending_names"

func (o *Owner) pendingNames() map[string]string {
	m := map[string]string{}
	if b, _ := o.st.get(kPendingNames); len(b) > 0 {
		json.Unmarshal(b, &m)
	}
	return m
}

func (o *Owner) setPendingName(machineID, name string) {
	o.pendingMu.Lock()
	defer o.pendingMu.Unlock()
	m := o.pendingNames()
	if name == "" {
		delete(m, machineID)
	} else {
		m[machineID] = name
	}
	b, _ := json.Marshal(m)
	o.st.set(kPendingNames, b)
}

// namePending names the machines approved under another name than the one
// they asked with, once they are online (run in the background). Nothing
// asks for that since approvals went (docs/accounts.md): it names what an
// older version left pending, and stops once nothing is, instead of
// reading the store every 3 s for good (docs/resident.md §4).
func (o *Owner) namePending(ctx context.Context) {
	for len(o.pendingNames()) > 0 {
		select {
		case <-ctx.Done():
			return
		case <-time.After(3 * time.Second):
		}
		pending := o.pendingNames()
		if len(pending) == 0 || !o.Initialized() {
			continue
		}
		machines, err := o.ListMachines(ctx)
		if err != nil {
			continue
		}
		for _, m := range machines {
			name, ok := pending[m.MachineId]
			if !ok || !m.Online {
				continue
			}
			if _, err := o.admin(ctx, m.MachineId, func(c *identityv1.AdminCommand) {
				c.Command = &identityv1.AdminCommand_SetName{SetName: &identityv1.SetName{Name: name}}
			}); err == nil {
				o.setPendingName(m.MachineId, "")
			}
		}
	}
}

func defaultSpec(spec *localv1.GrantSpec) *localv1.GrantSpec {
	s := proto.Clone(spec).(*localv1.GrantSpec)
	if len(s.MachineIds) == 0 {
		s.MachineIds = []string{"*"}
	}
	if len(s.ProjectIds) == 0 {
		s.ProjectIds = []string{"*"}
	}
	if len(s.Agents) == 0 {
		s.Agents = []string{"*"}
	}
	if s.TaskVisibility == identityv1.TaskVisibility_TASK_VISIBILITY_UNSPECIFIED {
		s.TaskVisibility = identityv1.TaskVisibility_TASK_VISIBILITY_OWN
	}
	if s.TtlDays > 90 {
		s.TtlDays = 90
	}
	return s
}

func grantFromSpec(base *identityv1.Grant, spec *localv1.GrantSpec) *identityv1.Grant {
	s := defaultSpec(spec)
	g := proto.Clone(base).(*identityv1.Grant)
	g.ClientLabel = s.Label
	g.MachineIds, g.ProjectIds, g.Agents = s.MachineIds, s.ProjectIds, s.Agents
	g.TaskVisibility, g.MaxConcurrentSessions, g.MachineExec = s.TaskVisibility, s.MaxConcurrentSessions, s.MachineExec
	g.NotBefore = timestamppb.New(time.Now().Add(-time.Minute))
	g.ExpiresAt = timestamppb.New(time.Now().Add(time.Duration(s.TtlDays) * 24 * time.Hour))
	if s.TtlDays == 0 { // the owner's own agents keep working until revoked
		g.ExpiresAt = timestamppb.New(identity.NoExpiry)
	}
	return g
}

// ListMachines lists the owner's machines as the relay knows them (see
// machines for the list with this device's own).
func (o *Owner) ListMachines(ctx context.Context) ([]*localv1.Machine, error) {
	ctl, _, err := o.control(ctx)
	if err != nil {
		return nil, err
	}
	res, err := ctl.ListMachines(ctx, &relayv1.ListMachinesRequest{})
	if err != nil {
		return nil, err
	}
	return o.machinesFrom(res), nil
}

// machinesFrom is the machines the relay listed (ListMachines, or
// WatchOwner) that are trustworthy, as this device shows them.
func (o *Owner) machinesFrom(res *relayv1.ListMachinesResponse) []*localv1.Machine {
	o.adoptChain(res.RootChain) // another device may have joined or left
	local := o.Status().LocalMachineId
	// What the relay last gave: a relay just started knows a machine's name
	// and when it was last online only once it is back (reported, lastSeen),
	// and the id is no name to show. One it last listed online was online
	// until about now, when the relay went.
	known := map[string]*localv1.Machine{}
	o.mu.Lock()
	for _, m := range o.lastMachines {
		known[m.MachineId] = m
	}
	o.mu.Unlock()
	wasOnline := o.seenOnline()
	var out []*localv1.Machine
	for _, e := range res.Machines {
		nc, _, err := o.trust.VerifyMachineCert(e.MachineCert)
		if err != nil {
			continue // not trustworthy; never shown
		}
		name := e.Name // as the machine reports it
		if pending, ok := o.pendingNames()[nc.MachineId]; ok {
			name = pending // set on it shortly (namePending)
		}
		k := known[nc.MachineId]
		if name == "" && k != nil {
			name = k.MachineName
		}
		if name == "" {
			name = nc.MachineId
		}
		seen := e.LastSeen
		if seen == nil && !e.Online {
			if slices.Contains(wasOnline, nc.MachineId) {
				seen = timestamppb.Now()
			} else if k != nil {
				seen = k.LastSeen
			}
		}
		out = append(out, &localv1.Machine{MachineId: nc.MachineId, MachineName: name, Online: e.Online, LastSeen: seen, Version: e.Version,
			Local: nc.MachineId == local, DeviceKeyId: nc.DeviceKeyId})
	}
	slices.SortFunc(out, func(a, b *localv1.Machine) int { return strings.Compare(a.MachineName, b.MachineName) })
	return out
}

// local is this device's own machine, in process, when it runs one: what
// concerns this Mac never goes through the relay.
func (o *Owner) local() *machine.Local {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.localMachine == nil {
		return nil
	}
	return o.localMachine.Local()
}

// machines lists the owner's machines: this device's own from in process,
// with its state (Online: whether the relay can reach it), and the others
// as the replica last polled them from the relay (see listedMachines).
func (o *Owner) machines(ctx context.Context) ([]*localv1.Machine, error) {
	s, err := o.rep.use(ctx)
	if err != nil {
		return nil, err
	}
	o.rep.mu.Lock()
	listed, err := listed(&s.machines)
	o.rep.mu.Unlock()
	if held(ctx) {
		err = nil // a screen shows them as last listed, offline (keepListed), whether or not the relay answers now
	}
	return o.withLocal(listed, err)
}

// listedMachines lists the owner's machines as the relay knows them. Without
// the relay, they are as it last listed them, offline, with its error.
func (o *Owner) listedMachines(ctx context.Context) ([]*localv1.Machine, error) {
	return o.keepListed(o.ListMachines(ctx))
}

// keepListed keeps what the relay listed, offline, for when it cannot be
// reached; then (err) it is what it last listed.
func (o *Owner) keepListed(listed []*localv1.Machine, err error) ([]*localv1.Machine, error) {
	o.mu.Lock()
	if err == nil {
		var off []*localv1.Machine
		var online []string
		for _, m := range listed {
			x := proto.CloneOf(m)
			x.Online = false
			off = append(off, x)
			if m.Online && !m.Local {
				online = append(online, m.MachineId)
			}
		}
		kept := &localv1.ListMachinesResponse{Machines: off}
		if !proto.Equal(kept, &localv1.ListMachinesResponse{Machines: o.lastMachines}) {
			b, _ := proto.Marshal(kept)
			o.st.set(kLastMachines, b)
		}
		o.lastMachines = off
		if v := strings.Join(online, ","); v != o.seen {
			o.seen = v
			o.st.set(kSeenOnline, []byte(v)) // for the next start's first connection (seenOnline)
		}
	} else {
		for _, m := range o.lastMachines {
			listed = append(listed, proto.CloneOf(m))
		}
	}
	o.mu.Unlock()
	return listed, err
}

// seenOnline is the other machines the relay last listed online (kept from
// the last start too): a new relay connection begins the end-to-end
// handshakes with them at once, ahead of the list that names them.
func (o *Owner) seenOnline() []string {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.seen == "" {
		return nil
	}
	return strings.Split(o.seen, ",")
}

// withLocal adds this device's own machine to the machines the relay
// listed; with it, the relay not answering is no error.
func (o *Owner) withLocal(listed []*localv1.Machine, err error) ([]*localv1.Machine, error) {
	l := o.local()
	if l == nil {
		return listed, err
	}
	info := l.Info()
	me := &localv1.Machine{MachineId: info.MachineId, MachineName: info.MachineName, Online: l.Connected(), Version: machine.Version, Local: true, Status: info.Status}
	out := []*localv1.Machine{me}
	for _, m := range listed {
		if m.MachineId == me.MachineId {
			me.LastSeen, me.DeviceKeyId = m.LastSeen, m.DeviceKeyId
		} else {
			out = append(out, m)
		}
	}
	slices.SortFunc(out, func(a, b *localv1.Machine) int { return strings.Compare(a.MachineName, b.MachineName) })
	return out, nil
}

// machineStatus gives each other online machine its state (coding agents,
// sessions, pausing), which the relay does not know, as the replica follows
// it end to end. One not followed (yet) is left without one.
func (o *Owner) machineStatus(machines []*localv1.Machine) {
	o.rep.feeds(func(feeds map[string]*feed) {
		for _, n := range machines {
			if f := feeds[n.MachineId]; n.Online && !n.Local && f != nil && f.ready { // this device's own has its state already
				n.Status = f.status
			}
		}
	})
}

func (o *Owner) findMachine(ctx context.Context, idOrName string) (*localv1.Machine, error) {
	machines, err := o.machines(ctx)
	if err != nil {
		return nil, err
	}
	for _, n := range machines {
		if n.MachineId == idOrName || n.MachineName == idOrName {
			return n, nil
		}
	}
	return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("no machine %q", idOrName))
}

// admin sends a machine an admin command. Settings (projects, pausing, name,
// agent defaults) grant nothing and go without a root signature, over this trusted
// device's own connection; anything else is signed (Touch ID).
func (o *Owner) admin(ctx context.Context, machineID string, build func(*identityv1.AdminCommand)) (string, error) {
	cmd := &identityv1.AdminCommand{OwnerId: o.ownerID, CommandId: identity.RandomCode(16), TargetMachineId: machineID,
		ExpiresAt: timestamppb.New(time.Now().Add(5 * time.Minute))}
	build(cmd)
	req := &machinev1.ApplyAdminCommandRequest{}
	switch cmd.Command.(type) {
	case *identityv1.AdminCommand_AddProject, *identityv1.AdminCommand_RemoveProject, *identityv1.AdminCommand_SetPaused, *identityv1.AdminCommand_SetName,
		*identityv1.AdminCommand_SetAgentDefaults, *identityv1.AdminCommand_SetAgentHidden:
		if l := o.local(); l != nil && l.MachineID() == machineID {
			return l.ApplySettings(ctx, build) // this device's own machine, in process
		}
		req.Settings = cmd
	default:
		signed, err := o.signRoot(ctx, cmd)
		if err != nil {
			return "", err
		}
		req.Command = signed[0]
	}
	c, err := o.relay(ctx)
	if err != nil {
		return "", err
	}
	n, err := c.Machine(ctx, machineID)
	if err != nil {
		return "", connect.NewError(connect.CodeUnavailable, err)
	}
	res, err := n.Admin.ApplyAdminCommand(ctx, req)
	if err != nil {
		c.ForgetMachine(n)
		return "", olderMachine(err)
	}
	return res.Message, nil
}

// machineClient reaches a machine end to end over this device's connection.
func (o *Owner) machineClient(ctx context.Context, machineID string) (*peer.Machine, error) {
	c, err := o.relay(ctx)
	if err != nil {
		return nil, err
	}
	n, err := c.Machine(ctx, machineID)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnavailable, err)
	}
	return n, nil
}

// RecheckAgents has a machine check its coding agents now (Check Again)
// and waits for it; the new state then reaches this device's replica.
func (o *Owner) RecheckAgents(ctx context.Context, machineID string, rescanHidden bool) error {
	n, err := o.machineClient(ctx, machineID)
	if err != nil {
		return err
	}
	_, err = n.Info.GetInfo(ctx, &machinev1.GetInfoRequest{Recheck: true, RescanHidden: rescanHidden})
	return err
}

// MachineRepos lists a machine's git repositories under its home folder,
// for Add a Project; this device's own machine is searched in process.
func (o *Owner) MachineRepos(ctx context.Context, machineID string) ([]*machinev1.Repo, error) {
	if l := o.local(); l != nil && l.MachineID() == machineID {
		return l.FindRepos(ctx), nil
	}
	n, err := o.machineClient(ctx, machineID)
	if err != nil {
		return nil, err
	}
	res, err := n.Admin.ListRepos(ctx, &machinev1.ListReposRequest{})
	if err != nil {
		return nil, err
	}
	return res.Repos, nil
}

// MachineFolders lists the folders in one of a machine's folders, for Add a
// Project; this device's own machine is read in process.
func (o *Owner) MachineFolders(ctx context.Context, machineID, path string, hidden bool) (*machinev1.ListFoldersResponse, error) {
	if l := o.local(); l != nil && l.MachineID() == machineID {
		return l.ListFolders(path, hidden)
	}
	n, err := o.machineClient(ctx, machineID)
	if err != nil {
		return nil, err
	}
	return n.Admin.ListFolders(ctx, &machinev1.ListFoldersRequest{Path: path, Hidden: hidden})
}

// TestMachineAgent runs Test with a coding agent on a machine.
func (o *Owner) TestMachineAgent(ctx context.Context, machineID, agent string) (*machinev1.TestAgentResponse, error) {
	if l := o.local(); l != nil && l.MachineID() == machineID {
		return l.TestAgent(ctx, agent)
	}
	n, err := o.machineClient(ctx, machineID)
	if err != nil {
		return nil, err
	}
	return n.Admin.TestAgent(ctx, &machinev1.TestAgentRequest{Agent: agent})
}

// MachineCustomAgents lists a machine's custom agents; this device's own
// machine is read in process.
func (o *Owner) MachineCustomAgents(ctx context.Context, machineID string) (*machinev1.ListCustomAgentsResponse, error) {
	if l := o.local(); l != nil && l.MachineID() == machineID {
		return l.CustomAgents(), nil
	}
	n, err := o.machineClient(ctx, machineID)
	if err != nil {
		return nil, err
	}
	return n.Admin.ListCustomAgents(ctx, &machinev1.ListCustomAgentsRequest{})
}

// SaveMachineCustomAgent adds or changes a custom agent on a machine.
func (o *Owner) SaveMachineCustomAgent(ctx context.Context, machineID string, a *machinev1.CustomAgent) error {
	if l := o.local(); l != nil && l.MachineID() == machineID {
		return l.SaveCustomAgent(a)
	}
	n, err := o.machineClient(ctx, machineID)
	if err != nil {
		return err
	}
	_, err = n.Admin.SaveCustomAgent(ctx, &machinev1.SaveCustomAgentRequest{Agent: a})
	return err
}

// RemoveMachineCustomAgent takes a custom agent off a machine.
func (o *Owner) RemoveMachineCustomAgent(ctx context.Context, machineID, name string) error {
	if l := o.local(); l != nil && l.MachineID() == machineID {
		return l.RemoveCustomAgent(name)
	}
	n, err := o.machineClient(ctx, machineID)
	if err != nil {
		return err
	}
	_, err = n.Admin.RemoveCustomAgent(ctx, &machinev1.RemoveCustomAgentRequest{Name: name})
	return err
}

// TestMachineCustomAgent starts an agent as given once on a machine and does
// the ACP handshake.
func (o *Owner) TestMachineCustomAgent(ctx context.Context, machineID string, a *machinev1.CustomAgent) (*machinev1.TestCustomAgentResponse, error) {
	if l := o.local(); l != nil && l.MachineID() == machineID {
		return l.TestCustomAgent(ctx, a)
	}
	n, err := o.machineClient(ctx, machineID)
	if err != nil {
		return nil, err
	}
	return n.Admin.TestCustomAgent(ctx, &machinev1.TestCustomAgentRequest{Agent: a})
}

// ListExecLog gathers the machines' ops logs (docs/machine-ops.md): the
// commands agents ran, or were refused, on each, newest first.
func (o *Owner) ListExecLog(ctx context.Context, machineID string) (*localv1.ListExecLogResponse, error) {
	on, off, err := o.onlineMachines(ctx, machineID)
	if err != nil {
		return nil, err
	}
	var mu sync.Mutex
	out := &localv1.ListExecLogResponse{Unreachable: off}
	add := func(res *machinev1.ListExecLogResponse, meta *localv1.Machine) {
		mu.Lock()
		defer mu.Unlock()
		for _, r := range res.Records {
			out.Entries = append(out.Entries, &localv1.ExecLogEntry{MachineId: meta.MachineId, MachineName: meta.MachineName, Record: r})
		}
	}
	bad, err := o.fanOut(ctx, on, func(l *machine.Local, meta *localv1.Machine) error {
		res, err := l.ExecLog()
		if err == nil {
			add(res, meta)
		}
		return err
	}, func(ctx context.Context, n *peer.Machine, meta *localv1.Machine) error {
		res, err := n.Admin.ListExecLog(ctx, &machinev1.ListExecLogRequest{})
		if err == nil {
			add(res, meta)
		}
		return err
	})
	out.Unreachable = append(out.Unreachable, bad...)
	slices.SortStableFunc(out.Entries, func(a, b *localv1.ExecLogEntry) int {
		return b.Record.StartedAt.AsTime().Compare(a.Record.StartedAt.AsTime())
	})
	return out, err
}

// ListDSHSessions gathers the DeepSeek Harness sessions Aide did not
// start on the machines, newest activity first. A machine that can't be
// reached has its sessions as last read here, marked lost: their state is
// last known, never taken for what they do now (docs/session-states.md).
func (o *Owner) ListDSHSessions(ctx context.Context, machineID string) (*localv1.ListDSHSessionsResponse, error) {
	on, off, err := o.onlineMachines(ctx, machineID)
	if err != nil {
		return nil, err
	}
	var mu sync.Mutex
	out := &localv1.ListDSHSessionsResponse{Unreachable: off}
	now := time.Now()
	add := func(res *machinev1.ListDSHSessionsResponse, meta *localv1.Machine) {
		entries := dshEntries(res, meta.MachineId, meta.MachineName, now)
		o.dshMu.Lock()
		if o.dshSeen == nil {
			o.dshSeen = map[string]dshSeen{}
		}
		o.dshSeen[meta.MachineId] = dshSeen{at: now, entries: entries}
		o.dshMu.Unlock()
		mu.Lock()
		defer mu.Unlock()
		out.Sessions = append(out.Sessions, entries...)
		if res.Bridge != nil || res.DshHome != "" {
			out.Bridges = append(out.Bridges, &localv1.DSHBridgeEntry{MachineId: meta.MachineId, Bridge: res.Bridge, DshHome: res.DshHome})
		}
	}
	bad, err := o.fanOut(ctx, on, func(l *machine.Local, meta *localv1.Machine) error {
		res, err := l.DSHSessions(ctx)
		if err == nil {
			add(res, meta)
		}
		return err
	}, func(ctx context.Context, n *peer.Machine, meta *localv1.Machine) error {
		res, err := n.Admin.ListDSHSessions(ctx, &machinev1.ListDSHSessionsRequest{})
		if err == nil {
			add(res, meta)
		}
		return err
	})
	if err != nil {
		// The relay can't be reached: neither can the other machines, whose
		// sessions are then lost, as Aide's own are; this one's still read.
		for _, n := range on {
			if !n.Local {
				bad = append(bad, &localv1.Unreachable{MachineId: n.MachineId, MachineName: n.MachineName, Error: err.Error()})
			}
		}
	}
	out.Unreachable = append(out.Unreachable, bad...)
	out.Sessions = append(out.Sessions, o.lostDSHEntries(out.Unreachable)...)
	slices.SortStableFunc(out.Sessions, func(a, b *localv1.DSHSessionEntry) int {
		return b.Session.LastActivityAt.AsTime().Compare(a.Session.LastActivityAt.AsTime())
	})
	slices.SortFunc(out.Bridges, func(a, b *localv1.DSHBridgeEntry) int { return strings.Compare(a.MachineId, b.MachineId) })
	return out, nil
}

func dshEntries(res *machinev1.ListDSHSessionsResponse, machineID, machineName string, readAt time.Time) []*localv1.DSHSessionEntry {
	var out []*localv1.DSHSessionEntry
	for _, s := range res.Sessions {
		out = append(out, &localv1.DSHSessionEntry{MachineId: machineID, MachineName: machineName, Session: s, ReadAt: timestamppb.New(readAt)})
	}
	return out
}

// lostDSHEntries is the DSH sessions last read of the machines in off,
// marked lost.
func (o *Owner) lostDSHEntries(off []*localv1.Unreachable) []*localv1.DSHSessionEntry {
	o.dshMu.Lock()
	defer o.dshMu.Unlock()
	var out []*localv1.DSHSessionEntry
	for _, u := range off {
		for _, e := range o.dshSeen[u.MachineId].entries {
			l := proto.Clone(e).(*localv1.DSHSessionEntry)
			l.Lost = true
			out = append(out, l)
		}
	}
	return out
}

// ListClaudeSessions gathers the Claude Code sessions the Claude desktop
// app lists on the machines (but those Aide started), newest activity
// first; a machine that can't be reached has them as last read here,
// lost, as ListDSHSessions does.
func (o *Owner) ListClaudeSessions(ctx context.Context, machineID string) (*localv1.ListClaudeSessionsResponse, error) {
	on, off, err := o.onlineMachines(ctx, machineID)
	if err != nil {
		return nil, err
	}
	var mu sync.Mutex
	out := &localv1.ListClaudeSessionsResponse{Unreachable: off}
	now := time.Now()
	add := func(res *machinev1.ListClaudeSessionsResponse, meta *localv1.Machine) {
		var entries []*localv1.ClaudeSessionEntry
		for _, s := range res.Sessions {
			entries = append(entries, &localv1.ClaudeSessionEntry{MachineId: meta.MachineId, MachineName: meta.MachineName, Session: s, ReadAt: timestamppb.New(now)})
		}
		o.dshMu.Lock()
		if o.claudeSeen == nil {
			o.claudeSeen = map[string]claudeSeen{}
		}
		o.claudeSeen[meta.MachineId] = claudeSeen{at: now, entries: entries}
		o.dshMu.Unlock()
		mu.Lock()
		defer mu.Unlock()
		out.Sessions = append(out.Sessions, entries...)
		out.Indexes = append(out.Indexes, &localv1.ClaudeIndexEntry{MachineId: meta.MachineId, Indexes: res.Indexes})
	}
	bad, err := o.fanOut(ctx, on, func(l *machine.Local, meta *localv1.Machine) error {
		res, err := l.ClaudeSessions()
		if err == nil {
			add(res, meta)
		}
		return err
	}, func(ctx context.Context, n *peer.Machine, meta *localv1.Machine) error {
		res, err := n.Admin.ListClaudeSessions(ctx, &machinev1.ListClaudeSessionsRequest{})
		if err == nil {
			add(res, meta)
		}
		return err
	})
	if err != nil {
		for _, n := range on {
			if !n.Local {
				bad = append(bad, &localv1.Unreachable{MachineId: n.MachineId, MachineName: n.MachineName, Error: err.Error()})
			}
		}
	}
	out.Unreachable = append(out.Unreachable, bad...)
	o.dshMu.Lock()
	for _, u := range out.Unreachable {
		for _, e := range o.claudeSeen[u.MachineId].entries {
			l := proto.Clone(e).(*localv1.ClaudeSessionEntry)
			l.Lost = true
			out.Sessions = append(out.Sessions, l)
		}
	}
	o.dshMu.Unlock()
	slices.SortStableFunc(out.Sessions, func(a, b *localv1.ClaudeSessionEntry) int {
		return b.Session.LastActivityAt.AsTime().Compare(a.Session.LastActivityAt.AsTime())
	})
	slices.SortFunc(out.Indexes, func(a, b *localv1.ClaudeIndexEntry) int { return strings.Compare(a.MachineId, b.MachineId) })
	return out, nil
}

// ReadClaudeSession reads one of them on a machine.
func (o *Owner) ReadClaudeSession(ctx context.Context, req *localv1.ReadClaudeSessionRequest) (*machinev1.ReadClaudeSessionResponse, error) {
	r := &machinev1.ReadClaudeSessionRequest{SessionId: req.SessionId, AfterSeq: req.AfterSeq, BeforeSeq: req.BeforeSeq, Limit: req.Limit, View: req.View}
	m, err := o.findMachine(ctx, req.MachineId)
	if err != nil {
		return nil, err
	}
	if l := o.local(); m.Local && l != nil {
		return l.ReadClaudeSession(r)
	}
	n, err := o.machineClient(ctx, m.MachineId)
	if err != nil {
		return nil, err
	}
	return n.Admin.ReadClaudeSession(ctx, r)
}

// ReadDSHSession reads a DeepSeek Harness session on a machine.
func (o *Owner) ReadDSHSession(ctx context.Context, req *localv1.ReadDSHSessionRequest) (*machinev1.ReadDSHSessionResponse, error) {
	m, err := o.findMachine(ctx, req.MachineId)
	if err != nil {
		return nil, err
	}
	if l := o.local(); m.Local && l != nil {
		return l.ReadDSHSession(dshRead(req))
	}
	n, err := o.machineClient(ctx, m.MachineId)
	if err != nil {
		return nil, err
	}
	return n.Admin.ReadDSHSession(ctx, dshRead(req))
}

// SetDSHTakeover, SendDSHMessage and InterruptDSHSession take over a DSH
// session on a machine (docs/dsh-takeover.md): this device's own in
// process, another's over the owner's connection to it.
func (o *Owner) SetDSHTakeover(ctx context.Context, req *localv1.SetDSHTakeoverRequest) (*machinev1.SetDSHTakeoverResponse, error) {
	r := &machinev1.SetDSHTakeoverRequest{SessionId: req.SessionId, On: req.On}
	return onDSHMachine(ctx, o, req.MachineId, func(l *machine.Local) (*machinev1.SetDSHTakeoverResponse, error) { return l.SetDSHTakeover(r) },
		func(n *peer.Machine) (*machinev1.SetDSHTakeoverResponse, error) {
			return n.Admin.SetDSHTakeover(ctx, r)
		})
}

func (o *Owner) SendDSHMessage(ctx context.Context, req *localv1.SendDSHMessageRequest) (*machinev1.SendDSHMessageResponse, error) {
	r := dshSend(req)
	return onDSHMachine(ctx, o, req.MachineId, func(l *machine.Local) (*machinev1.SendDSHMessageResponse, error) { return l.SendDSHMessage(ctx, r) },
		func(n *peer.Machine) (*machinev1.SendDSHMessageResponse, error) {
			return n.Admin.SendDSHMessage(ctx, r)
		})
}

func (o *Owner) InterruptDSHSession(ctx context.Context, req *localv1.InterruptDSHSessionRequest) (*machinev1.InterruptDSHSessionResponse, error) {
	r := &machinev1.InterruptDSHSessionRequest{SessionId: req.SessionId}
	return onDSHMachine(ctx, o, req.MachineId, func(l *machine.Local) (*machinev1.InterruptDSHSessionResponse, error) {
		return l.InterruptDSHSession(ctx, r)
	},
		func(n *peer.Machine) (*machinev1.InterruptDSHSessionResponse, error) {
			return n.Admin.InterruptDSHSession(ctx, r)
		})
}

func (o *Owner) PrepareDSHBridge(ctx context.Context, req *localv1.PrepareDSHBridgeRequest) (*machinev1.PrepareDSHBridgeResponse, error) {
	return onDSHMachine(ctx, o, req.MachineId, func(l *machine.Local) (*machinev1.PrepareDSHBridgeResponse, error) { return l.PrepareDSHBridge() },
		func(n *peer.Machine) (*machinev1.PrepareDSHBridgeResponse, error) {
			return n.Admin.PrepareDSHBridge(ctx, &machinev1.PrepareDSHBridgeRequest{})
		})
}

// onDSHMachine runs local on this device's own machine, remote on another.
func onDSHMachine[T any](ctx context.Context, o *Owner, machineID string, local func(*machine.Local) (T, error), remote func(*peer.Machine) (T, error)) (T, error) {
	var zero T
	m, err := o.findMachine(ctx, machineID)
	if err != nil {
		return zero, err
	}
	if l := o.local(); m.Local && l != nil {
		return local(l)
	}
	n, err := o.machineClient(ctx, m.MachineId)
	if err != nil {
		return zero, err
	}
	return remote(n)
}

// DSHSessionOptions are the models and permission presets DSH offers a
// taken-over session on a machine.
func (o *Owner) DSHSessionOptions(ctx context.Context, req *localv1.DSHSessionOptionsRequest) (*machinev1.DSHSessionOptionsResponse, error) {
	return onDSHMachine(ctx, o, req.MachineId, func(l *machine.Local) (*machinev1.DSHSessionOptionsResponse, error) {
		return l.DSHSessionOptions(ctx, req.SessionId)
	},
		func(n *peer.Machine) (*machinev1.DSHSessionOptionsResponse, error) {
			return n.Admin.DSHSessionOptions(ctx, &machinev1.DSHSessionOptionsRequest{SessionId: req.SessionId})
		})
}

func dshSend(req *localv1.SendDSHMessageRequest) *machinev1.SendDSHMessageRequest {
	return &machinev1.SendDSHMessageRequest{SessionId: req.SessionId, Text: req.Text, RequestId: req.RequestId, Model: req.Model, Permission: req.Permission}
}

func dshRead(req *localv1.ReadDSHSessionRequest) *machinev1.ReadDSHSessionRequest {
	return &machinev1.ReadDSHSessionRequest{SessionId: req.SessionId, AfterSeq: req.AfterSeq, BeforeSeq: req.BeforeSeq, Limit: req.Limit, View: req.View}
}

// RenameMachine gives a machine another name: a setting, sent over this
// device's own connection (no Touch ID).
func (o *Owner) RenameMachine(ctx context.Context, machineID, name string) error {
	name, err := renameTo(name)
	if err != nil {
		return err
	}
	n, err := o.findMachine(ctx, machineID)
	if err != nil {
		return err
	}
	machines, err := o.machines(ctx)
	if err != nil {
		return err
	}
	for _, m := range machines {
		if m.MachineName == name && m.MachineId != n.MachineId {
			return connect.NewError(connect.CodeAlreadyExists, fmt.Errorf("a machine named %q already exists", name))
		}
	}
	_, err = o.admin(ctx, n.MachineId, func(c *identityv1.AdminCommand) {
		c.Command = &identityv1.AdminCommand_SetName{SetName: &identityv1.SetName{Name: name}}
	})
	return err
}

func (o *Owner) SetMachinePaused(ctx context.Context, machineID string, paused bool) error {
	_, err := o.admin(ctx, machineID, func(c *identityv1.AdminCommand) {
		c.Command = &identityv1.AdminCommand_SetPaused{SetPaused: &identityv1.SetPaused{Paused: paused}}
	})
	return err
}

// SetAgentDefaults sets what a coding agent's sessions on a machine run
// with when the client names no model: a setting (no Touch ID).
func (o *Owner) SetAgentDefaults(ctx context.Context, machineID string, d *identityv1.AgentDefaults) error {
	_, err := o.admin(ctx, machineID, func(c *identityv1.AdminCommand) {
		c.Command = &identityv1.AdminCommand_SetAgentDefaults{SetAgentDefaults: d}
	})
	return err
}

// SetAgentHidden hides a coding agent a machine lists, or shows it again: a
// setting (no Touch ID), as agent defaults are.
func (o *Owner) SetAgentHidden(ctx context.Context, machineID, agent string, hidden bool) error {
	_, err := o.admin(ctx, machineID, func(c *identityv1.AdminCommand) {
		c.Command = &identityv1.AdminCommand_SetAgentHidden{SetAgentHidden: &identityv1.SetAgentHidden{Agent: agent, Hidden: hidden}}
	})
	return err
}

// RemoveMachine removes a machine; with eraseHer, the data Her left there
// (from before Aide dropped her) is erased first, which needs the machine to
// answer: otherwise nothing is removed.
func (o *Owner) RemoveMachine(ctx context.Context, machineID string, eraseHer bool) error {
	ctl, _, err := o.control(ctx)
	if err != nil {
		return err
	}
	if eraseHer {
		if err := o.eraseHerData(ctx, machineID); err != nil {
			return err
		}
	}
	// A grant Her held on that machine, from before Aide dropped her, goes with it.
	her := o.herGrants(ctx, machineID)
	if _, err := ctl.Revoke(ctx, &relayv1.RevokeRequest{Revocations: &identityv1.RevocationList{OwnerId: o.ownerID, RevokedMachineIds: []string{machineID}, RevokedGrantIds: her}}); err != nil {
		return err
	}
	for _, id := range her {
		o.st.setRevoked(id)
	}
	o.forgetHer(machineID)
	if o.Status().LocalMachineId == machineID {
		o.SetLocalMachine(ctx, false, "")
	}
	return nil
}

func (o *Owner) AddProject(ctx context.Context, req *localv1.AddProjectRequest) error {
	_, err := o.admin(ctx, req.MachineId, func(c *identityv1.AdminCommand) {
		c.Command = &identityv1.AdminCommand_AddProject{AddProject: &identityv1.AddProject{ProjectId: req.ProjectId, Path: req.Path, DefaultBaseRef: req.DefaultBaseRef, Workspace: req.Workspace,
			Description: req.Description, Agents: req.Agents}}
	})
	return err
}

func (o *Owner) RemoveProject(ctx context.Context, machineID, projectID string) error {
	_, err := o.admin(ctx, machineID, func(c *identityv1.AdminCommand) {
		c.Command = &identityv1.AdminCommand_RemoveProject{RemoveProject: &identityv1.RemoveProject{ProjectId: projectID}}
	})
	return err
}

// onlineMachines returns the machines that can be reached (optionally just
// one): this device's own always, the others when online.
func (o *Owner) onlineMachines(ctx context.Context, only string) ([]*localv1.Machine, []*localv1.Unreachable, error) {
	machines, err := o.machines(ctx)
	if err != nil {
		return nil, nil, err
	}
	var on []*localv1.Machine
	var off []*localv1.Unreachable
	for _, n := range machines {
		if only != "" && n.MachineId != only && n.MachineName != only {
			continue
		}
		if n.Online || n.Local {
			on = append(on, n)
		} else {
			off = append(off, &localv1.Unreachable{MachineId: n.MachineId, MachineName: n.MachineName, Error: "offline"})
		}
	}
	return on, off, nil
}

// fanOut runs remote on each machine over the relay, and local on this
// device's own in process (skipped when nil); the machines it fails on come
// back unreachable.
func (o *Owner) fanOut(ctx context.Context, machines []*localv1.Machine, local func(l *machine.Local, meta *localv1.Machine) error,
	remote func(ctx context.Context, n *peer.Machine, meta *localv1.Machine) error) ([]*localv1.Unreachable, error) {
	byID := map[string]*localv1.Machine{}
	var ids []string
	var bad []*localv1.Unreachable
	for _, n := range machines {
		if !n.Local {
			byID[n.MachineId] = n
			ids = append(ids, n.MachineId)
			continue
		}
		if l := o.local(); local != nil && l != nil {
			if err := local(l, n); err != nil {
				bad = append(bad, &localv1.Unreachable{MachineId: n.MachineId, MachineName: n.MachineName, Error: err.Error()})
			}
		}
	}
	if len(ids) == 0 {
		return bad, nil
	}
	c, err := o.relay(ctx)
	if err != nil {
		return bad, err
	}
	errs := c.FanOut(ctx, ids, func(ctx context.Context, pn *peer.Machine) error { return remote(ctx, pn, byID[pn.MachineID]) })
	for id, err := range errs {
		bad = append(bad, &localv1.Unreachable{MachineId: id, MachineName: byID[id].MachineName, Error: err.Error()})
	}
	return bad, nil
}

func (o *Owner) ListProjects(ctx context.Context, machineID string) (*localv1.ListProjectsResponse, error) {
	on, off, err := o.onlineMachines(ctx, machineID)
	if err != nil {
		return nil, err
	}
	var mu sync.Mutex
	out := &localv1.ListProjectsResponse{Unreachable: off}
	// The followed machines' projects are in the replica; the others are asked.
	var ask []*localv1.Machine
	o.rep.feeds(func(feeds map[string]*feed) {
		for _, n := range on {
			switch f := feeds[n.MachineId]; {
			case !n.Local && f != nil && (f.ready || held(ctx)):
				out.Projects = append(out.Projects, f.projects...) // as last known, when not followed now
			case n.Local || !held(ctx):
				ask = append(ask, n)
			}
		}
	})
	add := func(res *machinev1.ListProjectsResponse, meta *localv1.Machine) {
		mu.Lock()
		defer mu.Unlock()
		for _, p := range res.Projects {
			out.Projects = append(out.Projects, &localv1.Project{MachineId: meta.MachineId, ProjectId: p.ProjectId, DefaultBaseRef: p.DefaultBaseRef, Workspace: p.Workspace,
				Description: p.Description, Agents: p.Agents, Path: p.Path})
		}
	}
	bad, err := o.fanOut(ctx, ask, func(l *machine.Local, meta *localv1.Machine) error {
		res, err := l.ListProjects(ctx)
		if err == nil {
			add(res, meta)
		}
		return err
	}, func(ctx context.Context, n *peer.Machine, meta *localv1.Machine) error {
		res, err := n.Info.ListProjects(ctx, &machinev1.ListProjectsRequest{})
		if err == nil {
			add(res, meta)
		}
		return err
	})
	out.Unreachable = append(out.Unreachable, bad...)
	return out, err
}

// ---------------------------------------------------------------------------
// Agents.

func specFromGrant(g *identityv1.Grant) *localv1.GrantSpec {
	ttl := uint32(0)
	if g.ExpiresAt != nil && g.NotBefore != nil && !g.ExpiresAt.AsTime().Equal(identity.NoExpiry) {
		ttl = uint32(g.ExpiresAt.AsTime().Sub(g.NotBefore.AsTime()).Hours()/24 + 0.5)
	}
	return &localv1.GrantSpec{Label: g.ClientLabel, MachineIds: g.MachineIds, ProjectIds: g.ProjectIds, Agents: g.Agents,
		TaskVisibility: g.TaskVisibility, TtlDays: ttl, MaxConcurrentSessions: g.MaxConcurrentSessions, MachineExec: g.MachineExec}
}

// ListClients lists the owner's agents, as the replica last polled them.
func (o *Owner) ListClients(ctx context.Context) ([]*localv1.Client, error) {
	s, err := o.rep.use(ctx)
	if err != nil {
		return nil, err
	}
	o.rep.mu.Lock()
	list, err := listed(&s.clients)
	o.rep.mu.Unlock()
	if err != nil && held(ctx) { // a screen shows them as last listed while the relay is away
		if len(list) == 0 {
			list = o.keptClients()
		}
		err = nil
	}
	return list, err
}

// listClients asks the relay for the owner's agents.
func (o *Owner) listClients(ctx context.Context) ([]*localv1.Client, error) {
	ctl, _, err := o.control(ctx)
	if err != nil {
		return nil, err
	}
	res, err := ctl.ListClients(ctx, &relayv1.ListClientsRequest{})
	if err != nil {
		return nil, err
	}
	return o.keepClients(o.clientsFrom(res)), nil
}

// keepClients keeps the agents the relay listed, for when it cannot be
// reached (lastClients).
func (o *Owner) keepClients(list []*localv1.Client) []*localv1.Client {
	o.mu.Lock()
	defer o.mu.Unlock()
	kept := &localv1.ListClientsResponse{Clients: list}
	if !proto.Equal(kept, &localv1.ListClientsResponse{Clients: o.lastClients}) {
		b, _ := proto.Marshal(kept)
		o.st.set(kLastClients, b)
		o.lastClients = list
	}
	return list
}

// keptClients is the agents as the relay last listed them.
func (o *Owner) keptClients() []*localv1.Client {
	o.mu.Lock()
	defer o.mu.Unlock()
	out := make([]*localv1.Client, 0, len(o.lastClients))
	for _, c := range o.lastClients {
		out = append(out, proto.CloneOf(c))
	}
	return out
}

// clientsFrom is the agents the relay listed (ListClients, or WatchOwner)
// whose grants this owner signed; those revoked here are marked revoked, as
// the relay's own: it can keep listing a grant it was never told of the end
// of (a revocation that reached the machines but not it), and such a one
// showed beside the grant that replaced it, "Her on <machine>" twice.
func (o *Owner) clientsFrom(res *relayv1.ListClientsResponse) []*localv1.Client {
	return clientsFrom(res, o.ownerID, o.trust, o.revokedHere())
}

// revokedHere are the grants this device knows are revoked: those it
// revoked, and those the owner's revocations its own machine applied name.
func (o *Owner) revokedHere() map[string]bool {
	out := map[string]bool{}
	if rows, err := o.st.grants(); err == nil {
		for _, r := range rows {
			if r.revoked && r.grant != nil {
				out[r.grant.GrantId] = true
			}
		}
	}
	if l := o.local(); l != nil {
		for _, id := range l.RevokedGrants() {
			out[id] = true
		}
	}
	return out
}

func clientsFrom(res *relayv1.ListClientsResponse, ownerID string, trust *identity.Trust, revoked map[string]bool) []*localv1.Client {
	byGrant := map[string]*localv1.Client{}
	var out []*localv1.Client
	for _, c := range res.Clients {
		g, _, err := trust.VerifyGrant(c.Grant, time.Now())
		expired := false
		if err != nil {
			// Expired grants still show up so they can be renewed.
			g = &identityv1.Grant{}
			if identity.Unverified(c.Grant, g) != nil || g.OwnerId != ownerID {
				continue
			}
			expired = true
		}
		a := &localv1.Client{ClientId: g.ClientId, GrantId: g.GrantId, Label: g.ClientLabel, Fingerprint: identity.ShortFingerprint(g.ClientPublicKey),
			Spec: specFromGrant(g), GrantVersion: g.Version, ExpiresAt: g.ExpiresAt, Online: c.Online && !expired, LastSeen: c.LastSeen, Revoked: c.Revoked || revoked[g.GrantId],
			Custodian: g.Custodian, Icon: g.ClientIcon, Image: g.ClientImage, HerMachineId: g.HerMachineId}
		if len(c.MachineIds) > 0 { // without removed machines; an update signs them away
			a.Spec.MachineIds = c.MachineIds
		}
		if prev := byGrant[g.GrantId]; prev == nil || prev.GrantVersion < a.GrantVersion {
			byGrant[g.GrantId] = a
		}
	}
	for _, a := range byGrant {
		out = append(out, a)
	}
	slices.SortFunc(out, func(a, b *localv1.Client) int { return strings.Compare(a.Label, b.Label) })
	return out
}

func (o *Owner) latestGrant(ctx context.Context, grantID string) (*identityv1.Grant, error) {
	ctl, _, err := o.control(ctx)
	if err != nil {
		return nil, err
	}
	res, err := ctl.ListClients(ctx, &relayv1.ListClientsRequest{})
	if err != nil {
		return nil, err
	}
	var best *identityv1.Grant
	for _, c := range res.Clients {
		g := &identityv1.Grant{}
		if identity.Unverified(c.Grant, g) != nil || g.GrantId != grantID {
			continue
		}
		if best == nil || g.Version > best.Version {
			best = g
		}
	}
	if best == nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("no agent with grant %s", grantID))
	}
	return best, nil
}

// UpdateClient re-issues the grant with a new spec. Older versions are
// invalidated immediately (they may have been more permissive).
func (o *Owner) UpdateClient(ctx context.Context, grantID string, spec *localv1.GrantSpec) (uint64, []*localv1.MachineAck, error) {
	cur, err := o.latestGrant(ctx, grantID)
	if err != nil {
		return 0, nil, err
	}
	if cur.HerMachineId != "" {
		// docs/her-mesh.md §3.2: it has no scope and no expiry to change.
		return 0, nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("Her's access is whole and doesn't expire: turn Her off to end it"))
	}
	return o.publishGrant(ctx, grantFromSpec(cur, spec), cur.Version)
}

// RenameClient gives an agent another name: its grant, signed again with
// the label alone changed (what it may use, and until when, stay).
// RenameClient gives a grant a new name and, when icon is not nil, a new
// icon (an image with "image").
func (o *Owner) RenameClient(ctx context.Context, grantID, label string, icon *string, img []byte) (uint64, error) {
	label = strings.Join(strings.FieldsFunc(label, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }), " ")
	if label == "" || len(label) > 64 {
		return 0, connect.NewError(connect.CodeInvalidArgument, errors.New("a name is 1 to 64 bytes"))
	}
	if icon != nil {
		if !grantIcons.MatchString(*icon) {
			return 0, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("unknown icon %q", *icon))
		}
		if *icon != "image" {
			img = nil
		} else if len(img) == 0 {
			return 0, connect.NewError(connect.CodeInvalidArgument, errors.New("the icon \"image\" needs a picture"))
		} else {
			var err error
			if img, err = grantImage(img); err != nil {
				return 0, err
			}
		}
	}
	cur, err := o.latestGrant(ctx, grantID)
	if err != nil {
		return 0, err
	}
	g := proto.Clone(cur).(*identityv1.Grant)
	g.ClientLabel = label
	if icon != nil {
		g.ClientIcon, g.ClientImage = *icon, img
	}
	v, _, err := o.publishGrant(ctx, g, cur.Version)
	return v, err
}

// publishGrant signs g as the version after cur and publishes it; machines
// then take no older one.
func (o *Owner) publishGrant(ctx context.Context, g *identityv1.Grant, cur uint64) (uint64, []*localv1.MachineAck, error) {
	grantID := g.GrantId
	g.Version = cur + 1
	signed, err := o.signRoot(ctx, g)
	if err != nil {
		return 0, nil, err
	}
	ctl, _, err := o.control(ctx)
	if err != nil {
		return 0, nil, err
	}
	if _, err := ctl.PublishGrant(ctx, &relayv1.PublishGrantRequest{Grant: signed[0]}); err != nil {
		return 0, nil, err
	}
	o.st.putGrant(signed[0], false)
	go o.refreshCards()
	acks, err := o.revoke(ctx, &identityv1.RevocationList{OwnerId: o.ownerID, MinGrantVersions: map[string]uint64{grantID: g.Version}})
	return g.Version, acks, err
}

func (o *Owner) RevokeClient(ctx context.Context, grantID string) ([]*localv1.MachineAck, error) {
	acks, err := o.revoke(ctx, &identityv1.RevocationList{OwnerId: o.ownerID, RevokedGrantIds: []string{grantID}})
	if err == nil {
		o.st.setRevoked(grantID)
	}
	return acks, err
}

// revoke tells the relay (which disconnects affected peers at once) and
// then pushes the list to every online machine over E2E, so a relay that
// swallows revocations is noticed.
func (o *Owner) revoke(ctx context.Context, r *identityv1.RevocationList) ([]*localv1.MachineAck, error) {
	ctl, _, err := o.control(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := ctl.Revoke(ctx, &relayv1.RevokeRequest{Revocations: r}); err != nil {
		return nil, err
	}
	machines, err := o.machines(ctx)
	if err != nil {
		return nil, err
	}
	var on []*localv1.Machine
	var acks []*localv1.MachineAck
	for _, n := range machines {
		if n.Online || n.Local {
			on = append(on, n)
		} else {
			acks = append(acks, &localv1.MachineAck{MachineId: n.MachineId, MachineName: n.MachineName, Error: "offline; it will apply the change when it reconnects"})
		}
	}
	var mu sync.Mutex
	applied := func(meta *localv1.Machine) {
		mu.Lock()
		acks = append(acks, &localv1.MachineAck{MachineId: meta.MachineId, MachineName: meta.MachineName, Ok: true})
		mu.Unlock()
	}
	bad, err := o.fanOut(ctx, on, func(l *machine.Local, meta *localv1.Machine) error {
		l.PushRevocations(r, o.trust.RootChain())
		applied(meta)
		return nil
	}, func(ctx context.Context, n *peer.Machine, meta *localv1.Machine) error {
		_, err := n.Admin.PushRevocations(ctx, &machinev1.PushRevocationsRequest{Revocations: r, RootChain: o.trust.RootChain()})
		if err == nil {
			applied(meta)
		}
		return err
	})
	for _, b := range bad {
		acks = append(acks, &localv1.MachineAck{MachineId: b.MachineId, MachineName: b.MachineName, Error: b.Error})
	}
	return acks, err
}

// ---------------------------------------------------------------------------
// Sessions.

// ListSessions takes the sessions of every reachable machine (or one), or
// its page of them, and merges the pages into one. In the BASIC view a
// followed machine's first page is in the replica (its newest
// keepSessions, all of them when it has no more); the others are asked.
func (o *Owner) ListSessions(ctx context.Context, req *localv1.ListSessionsRequest) (*localv1.ListSessionsResponse, error) {
	on, off, err := o.onlineMachines(ctx, req.MachineId)
	if err != nil {
		return nil, err
	}
	var mu sync.Mutex
	more := false
	out := &localv1.ListSessionsResponse{Unreachable: off}
	basic := req.View == sessionv1.SessionView_SESSION_VIEW_BASIC
	var ask []*localv1.Machine
	o.rep.feeds(func(feeds map[string]*feed) {
		for _, n := range on {
			f := feeds[n.MachineId]
			if !n.Local && held(ctx) && (f == nil || !f.ready) {
				continue // a screen's: those it does not follow now come with what it held of them
			}
			if n.Local || f == nil || !f.ready || !basic || req.Before != nil || f.more && (req.Limit == 0 || req.Limit > keepSessions) {
				ask = append(ask, n)
				continue
			}
			for _, s := range f.sessions {
				out.Sessions = append(out.Sessions, s)
			}
			more = more || f.more
		}
	})
	page := &sessionv1.ListSessionsRequest{Limit: req.Limit, Before: req.Before, View: req.View}
	add := func(res *sessionv1.ListSessionsResponse) {
		if basic {
			res.Sessions = machine.BasicSessions(res.Sessions) // a machine from before views sends them whole
		}
		mu.Lock()
		out.Sessions = append(out.Sessions, res.Sessions...)
		more = more || res.Next != nil
		mu.Unlock()
	}
	bad, err := o.fanOut(ctx, ask, func(l *machine.Local, _ *localv1.Machine) error {
		res, err := l.ListSessions(page)
		if err == nil {
			add(res)
		}
		return err
	}, func(ctx context.Context, n *peer.Machine, _ *localv1.Machine) error {
		res, err := n.Session.ListSessions(ctx, page)
		if err == nil {
			add(res)
		}
		return err
	})
	out.Unreachable = append(out.Unreachable, bad...)
	if req.Limit > 0 {
		out.Sessions, out.Next = onePage(out.Sessions, int(req.Limit), req.Before, more)
	} else {
		slices.SortFunc(out.Sessions, func(a, b *sessionv1.Session) int { return b.UpdatedAt.AsTime().Compare(a.UpdatedAt.AsTime()) })
	}
	return out, err
}

// onePage cuts the machines' pages (each machine's newest past before, a
// first page with its unsettled sessions too) to the newest limit across
// them, plus on a first page the unsettled ones, and says where the next
// page starts: just past the last one kept, when some machine has more or
// some were cut. A machine too old to page answers with all its sessions.
func onePage(sessions []*sessionv1.Session, limit int, before *sessionv1.SessionCursor, more bool) ([]*sessionv1.Session, *sessionv1.SessionCursor) {
	slices.SortFunc(sessions, machine.NewestFirst)
	var page, unsettled []*sessionv1.Session
	for _, s := range sessions {
		if !machine.Past(s, before) {
			continue
		}
		if len(page) < limit {
			page = append(page, s)
			continue
		}
		more = true // the next page brings it
		if before == nil && machine.Unsettled(s) {
			unsettled = append(unsettled, s)
		}
	}
	var next *sessionv1.SessionCursor
	if more && len(page) > 0 {
		next = machine.CursorAt(page[len(page)-1])
	}
	return append(page, unsettled...), next
}

// localSession is this device's own machine when it owns the session (by
// the session id's prefix), else nil.
func (o *Owner) localSession(sessionID string) *machine.Local {
	l := o.local()
	parts := strings.SplitN(sessionID, "_", 3)
	if l == nil || len(parts) != 3 || parts[0] != "s" || !strings.HasPrefix(strings.TrimPrefix(l.MachineID(), "m_"), parts[1]) {
		return nil
	}
	return l
}

// sessionMachine finds the machine that owns a session from its id prefix.
func (o *Owner) sessionMachine(ctx context.Context, sessionID string) (*peer.Machine, error) {
	if parts := strings.SplitN(sessionID, "_", 3); len(parts) != 3 || parts[0] != "s" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("bad session id"))
	}
	machines, err := o.machines(ctx)
	if err != nil {
		return nil, err
	}
	for _, n := range machines {
		if ownsSession(n.MachineId, sessionID) {
			c, err := o.relay(ctx)
			if err != nil {
				return nil, err
			}
			pn, err := c.Machine(ctx, n.MachineId)
			if err != nil {
				return nil, connect.NewError(connect.CodeUnavailable, fmt.Errorf("%s is unreachable: %w", n.MachineName, err))
			}
			return pn, nil
		}
	}
	return nil, connect.NewError(connect.CodeNotFound, errors.New("no machine owns this session"))
}

func (o *Owner) StreamSessionEvents(ctx context.Context, req *localv1.StreamSessionEventsRequest, send func(*sessionv1.SessionEvent) error) error {
	ask := &sessionv1.StreamEventsRequest{SessionId: req.SessionId, AfterSeq: req.AfterSeq, Follow: req.Follow, Partial: req.Partial, View: req.View}
	if l := o.localSession(req.SessionId); l != nil {
		return l.StreamEvents(ctx, ask, send)
	}
	n, err := o.sessionMachine(ctx, req.SessionId)
	if err != nil {
		return err
	}
	if req.View == sessionv1.EventView_EVENT_VIEW_BASIC { // a machine from before views sends events whole
		full := send
		send = func(e *sessionv1.SessionEvent) error { return full(machine.BasicEvent(e)) }
	}
	stream, err := n.Session.StreamEvents(ctx, ask)
	if err != nil {
		return err
	}
	defer stream.Close()
	for stream.Receive() {
		if err := send(stream.Msg()); err != nil {
			return err
		}
	}
	return stream.Err()
}

// ListSessionEvents pages through a session's events on its machine.
func (o *Owner) ListSessionEvents(ctx context.Context, req *sessionv1.ListEventsRequest) (*sessionv1.ListEventsResponse, error) {
	if l := o.localSession(req.SessionId); l != nil {
		return l.ListEvents(req)
	}
	n, err := o.sessionMachine(ctx, req.SessionId)
	if err != nil {
		return nil, err
	}
	res, err := n.Session.ListEvents(ctx, req)
	if err == nil && req.View == sessionv1.EventView_EVENT_VIEW_BASIC {
		for i, e := range res.Events {
			res.Events[i] = machine.BasicEvent(e)
		}
	}
	return res, err
}

// ReadToolOutput streams a tool's whole output from its session's machine.
// One from before ReadToolOutput gives it as the event at req.Seq recorded
// it (its first 16 KiB).
func (o *Owner) ReadToolOutput(ctx context.Context, req *localv1.ReadToolOutputRequest, send func(*sessionv1.ToolOutputChunk) error) error {
	ask := &sessionv1.ReadToolOutputRequest{SessionId: req.SessionId, CallId: req.CallId}
	if l := o.localSession(req.SessionId); l != nil {
		return l.ReadToolOutput(ask, send)
	}
	n, err := o.sessionMachine(ctx, req.SessionId)
	if err != nil {
		return err
	}
	stream, err := n.Session.ReadToolOutput(ctx, ask)
	if err != nil {
		return err
	}
	defer stream.Close()
	for stream.Receive() {
		if err := send(stream.Msg()); err != nil {
			return err
		}
	}
	if connect.CodeOf(stream.Err()) != connect.CodeUnimplemented || req.Seq == 0 {
		return stream.Err()
	}
	res, err := n.Session.ListEvents(ctx, &sessionv1.ListEventsRequest{SessionId: req.SessionId, AfterSeq: req.Seq - 1, Limit: 1})
	if err != nil {
		return err
	}
	for _, e := range res.Events {
		if r := e.GetToolResult(); r != nil && r.CallId == req.CallId {
			return send(&sessionv1.ToolOutputChunk{Data: []byte(r.Summary), Size: uint64(len(r.Summary))})
		}
	}
	return connect.NewError(connect.CodeNotFound, fmt.Errorf("no output of %s at %d", req.CallId, req.Seq))
}

// StartSession starts a session on a machine as the owner: this device's
// own in process, another end to end.
func (o *Owner) StartSession(ctx context.Context, machineID string, req *sessionv1.StartSessionRequest) (*sessionv1.Session, error) {
	if l := o.local(); l != nil && l.MachineID() == machineID {
		return l.Start(ctx, req)
	}
	n, err := o.machineClient(ctx, machineID)
	if err != nil {
		return nil, err
	}
	res, err := n.Session.StartSession(ctx, req)
	if err != nil {
		return nil, err
	}
	return o.written(res.Session.SessionId)(res.Session, nil)
}

// SendMessage sends a message to a session the owner started.
func (o *Owner) SendMessage(ctx context.Context, id, text string, atts []*sessionv1.Attachment) (*sessionv1.Session, error) {
	if l := o.localSession(id); l != nil {
		return l.Send(ctx, id, text, atts)
	}
	n, err := o.sessionMachine(ctx, id)
	if err != nil {
		return nil, err
	}
	res, err := n.Session.SendMessage(ctx, &sessionv1.SendMessageRequest{SessionId: id, Text: text, Attachments: atts})
	if err != nil {
		return nil, err
	}
	return o.written(id)(res.Session, nil)
}

func (o *Owner) InterruptSession(ctx context.Context, id string) (*sessionv1.Session, error) {
	if l := o.localSession(id); l != nil {
		return l.Interrupt(ctx, id)
	}
	n, err := o.sessionMachine(ctx, id)
	if err != nil {
		return nil, err
	}
	return o.written(id)(n.Session.InterruptSession(ctx, &sessionv1.InterruptSessionRequest{SessionId: id}))
}

func (o *Owner) ArchiveSession(ctx context.Context, id string) (*sessionv1.Session, error) {
	if l := o.localSession(id); l != nil {
		return l.Archive(ctx, id)
	}
	n, err := o.sessionMachine(ctx, id)
	if err != nil {
		return nil, err
	}
	return o.written(id)(n.Session.ArchiveSession(ctx, &sessionv1.ArchiveSessionRequest{SessionId: id}))
}

func (o *Owner) UnarchiveSession(ctx context.Context, id string) (*sessionv1.Session, error) {
	if l := o.localSession(id); l != nil {
		return l.Unarchive(ctx, id)
	}
	n, err := o.sessionMachine(ctx, id)
	if err != nil {
		return nil, err
	}
	return o.written(id)(n.Session.UnarchiveSession(ctx, &sessionv1.UnarchiveSessionRequest{SessionId: id}))
}

// DeleteSession deletes a session for good on its machine.
func (o *Owner) DeleteSession(ctx context.Context, id string) error {
	if l := o.localSession(id); l != nil {
		return l.Delete(ctx, id)
	}
	n, err := o.sessionMachine(ctx, id)
	if err != nil {
		return err
	}
	if _, err = n.Session.DeleteSession(ctx, &sessionv1.DeleteSessionRequest{SessionId: id}); err == nil {
		o.rep.sessionWritten(id, nil, true)
	}
	return err
}

// written keeps in the replica what a change to another machine's session
// returned, and passes it on.
func (o *Owner) written(id string) func(*sessionv1.Session, error) (*sessionv1.Session, error) {
	return func(s *sessionv1.Session, err error) (*sessionv1.Session, error) {
		if err == nil {
			o.rep.sessionWritten(id, s, false)
		}
		return s, err
	}
}

// RenameSession changes a session's title on its machine.
func (o *Owner) RenameSession(ctx context.Context, id, title string) (*sessionv1.Session, error) {
	if l := o.localSession(id); l != nil {
		return l.Rename(ctx, id, title)
	}
	n, err := o.sessionMachine(ctx, id)
	if err != nil {
		return nil, err
	}
	return o.written(id)(n.Session.RenameSession(ctx, &sessionv1.RenameSessionRequest{SessionId: id, Title: title}))
}

// RollbackSession rolls a session back to an earlier turn on its machine.
func (o *Owner) RollbackSession(ctx context.Context, id string, turn uint32) (*sessionv1.Session, error) {
	if l := o.localSession(id); l != nil {
		return l.Rollback(ctx, id, turn)
	}
	n, err := o.sessionMachine(ctx, id)
	if err != nil {
		return nil, err
	}
	return o.written(id)(n.Session.RollbackSession(ctx, &sessionv1.RollbackSessionRequest{SessionId: id, Turn: turn}))
}

// MarkSessionSeen records on a session's machine what the owner has seen of it.
func (o *Owner) MarkSessionSeen(ctx context.Context, req *sessionv1.MarkSessionSeenRequest) (*sessionv1.Session, error) {
	if l := o.localSession(req.SessionId); l != nil {
		return l.MarkSeen(ctx, req)
	}
	n, err := o.sessionMachine(ctx, req.SessionId)
	if err != nil {
		return nil, err
	}
	return o.written(req.SessionId)(n.Session.MarkSessionSeen(ctx, req))
}

// AnswerQuestion answers a session's question as the owner.
func (o *Owner) AnswerQuestion(ctx context.Context, req *sessionv1.AnswerQuestionRequest) (*sessionv1.Session, error) {
	if l := o.localSession(req.SessionId); l != nil {
		return l.AnswerQuestion(ctx, req)
	}
	n, err := o.sessionMachine(ctx, req.SessionId)
	if err != nil {
		return nil, err
	}
	return o.written(req.SessionId)(n.Session.AnswerQuestion(ctx, req))
}

func (o *Owner) GetDiff(ctx context.Context, id string, turn uint32) (*sessionv1.GetDiffResponse, error) {
	if l := o.localSession(id); l != nil {
		return l.Diff(ctx, &sessionv1.GetDiffRequest{SessionId: id, Turn: turn})
	}
	n, err := o.sessionMachine(ctx, id)
	if err != nil {
		return nil, err
	}
	return n.Session.GetDiff(ctx, &sessionv1.GetDiffRequest{SessionId: id, Turn: turn})
}

// GetSessionInsights reviews a session (docs/session-insights.md), as the
// owner.
func (o *Owner) GetSessionInsights(ctx context.Context, id string) (*sessionv1.SessionInsights, error) {
	req := &sessionv1.GetSessionInsightsRequest{SessionId: id}
	if l := o.localSession(id); l != nil {
		return l.Insights(ctx, req)
	}
	n, err := o.sessionMachine(ctx, id)
	if err != nil {
		return nil, err
	}
	return n.Session.GetSessionInsights(ctx, req)
}

// PushSession pushes a session's branch to its repository's remote, as
// the owner asked.
func (o *Owner) PushSession(ctx context.Context, id string) (*sessionv1.PushSessionResponse, error) {
	if l := o.localSession(id); l != nil {
		return l.Push(ctx, id)
	}
	n, err := o.sessionMachine(ctx, id)
	if err != nil {
		return nil, err
	}
	return n.Session.PushSession(ctx, &sessionv1.PushSessionRequest{SessionId: id})
}
