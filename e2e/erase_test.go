package e2e

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	localv1 "github.com/samir2015dzh/aide/gen/go/aide/local/v1"
)

// TestEraseEverything: from one of the owner's devices (the server signs
// the order), the owner erases Aide everywhere (docs/erase-everything.md).
// The other device, a Mac that is only a machine, a machine on a server and
// the agent each erase themselves when the relay refuses them; a machine
// that was off does so when it next starts. A worktree with uncommitted
// changes is kept, a clean one is not. The relay can be claimed again, and
// the Mac set up from scratch.
func TestEraseEverything(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e")
	}
	env := []string{"AIDE_FAKE_PROVIDERS=1"}
	// Homes of a her on the Mac and on the servers. Aide no longer has Her, so
	// she was never on on any of these machines: erasing everything must leave
	// them alone (a machine where she was on once erases hers: the machine
	// package's TestEraseHer).
	herMac, herVPS := filepath.Join(t.TempDir(), "her"), filepath.Join(t.TempDir(), "her")
	for _, d := range []string{herMac, herVPS} {
		os.MkdirAll(filepath.Join(d, "bots"), 0o700)
		os.WriteFile(filepath.Join(d, ".her-home"), nil, 0o600) // as her marks a home she makes
	}
	herEnv := func(home string) []string {
		return append(slices.Clone(env), "HER_HOME="+home, "HER_CREDENTIAL_STORE=file")
	}
	w := newOwnerMac(t, herEnv(herMac), &localv1.GrantSpec{})
	ctx := context.Background()
	daemon := filepath.Join(w.bin, "aided")

	// Two sessions on the Mac's machine: one leaves its worktree with
	// uncommitted changes, the other leaves it clean.
	dirty, r := w.startSession(time.Minute, "--wait", "--prompt", "say working")
	if dirty.Status != "idle" {
		t.Fatalf("dirty session: %+v", r)
	}
	dirtyWT := w.worktree(dirty.SessionID)
	if err := os.WriteFile(filepath.Join(dirtyWT, "unsaved.txt"), []byte("work in progress\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	clean, r := w.startSession(time.Minute, "--wait", "--prompt", "say done")
	if clean.Status != "idle" {
		t.Fatalf("clean session: %+v", r)
	}
	cleanWT := w.worktree(clean.SessionID)
	if _, err := os.Stat(cleanWT); err != nil {
		t.Fatal(err)
	}

	// Two machines on servers: vps-on runs, vps-off is off when the owner
	// erases everything.
	pairVPS := func(name string) (data string) {
		data = filepath.Join(w.tmp, name)
		w.loginMachine(name, data, env)
		return data
	}
	runVPS := func(name, data string) *proc {
		p := start(t, name, herEnv(herVPS), daemon, "run", "--data-dir", data)
		w.logs = append(w.logs, p)
		return p
	}
	online := func(name string) bool {
		res, err := w.app.client.ListMachines(ctx, &localv1.ListMachinesRequest{})
		return err == nil && slices.ContainsFunc(res.Machines, func(m *localv1.Machine) bool { return m.MachineName == name && m.Online })
	}
	onData, offData := pairVPS("vps-on"), pairVPS("vps-off")
	// Logged in, and never used again until the owner is a new one: a
	// machine and an agent the erase never reaches.
	lateData := pairVPS("vps-late")
	lateAgent := w.addAgent("late", &localv1.GrantSpec{})
	// On a Mac the app's owner shares the data directory: it stays.
	appFile := filepath.Join(onData, "owner", "profiles", "p1", "owner.db")
	os.MkdirAll(filepath.Dir(appFile), 0o700)
	os.WriteFile(appFile, []byte("another owner"), 0o600)
	machineErased := func(data string) {
		t.Helper()
		if _, err := os.Stat(filepath.Join(data, "machine")); !os.IsNotExist(err) {
			t.Fatalf("%s kept its machine: %v", data, err)
		}
	}
	vpsOn, vpsOff, vpsLate := runVPS("vps-on", onData), runVPS("vps-off", offData), runVPS("vps-late", lateData)
	eventually(t, 30*time.Second, "the servers online", func() bool { return online("vps-on") && online("vps-off") && online("vps-late") })
	vpsOff.stop()
	vpsLate.stop()

	// A Mac that is only a machine, and a second device of the owner's.
	mac3, _ := w.otherMac("mac3", env)
	home, _ := w.app.client.GetStatus(ctx, &localv1.GetStatusRequest{})
	w.logInMachineOnly(mac3, home, "mac3")
	_, mac2Sock := w.otherMac("mac2", env)
	app2 := newTestApp(t, mac2Sock)
	if _, err := app2.client.LogIn(ctx, &localv1.LogInRequest{RelayUrl: home.RelayUrl, Account: testAccount, Password: testPassword, Label: "mac2", Platform: "macos"}); err != nil {
		t.Fatal(err)
	}
	eventually(t, 30*time.Second, "mac2 sees the machines", func() bool {
		res, err := app2.client.ListMachines(ctx, &localv1.ListMachinesRequest{})
		return err == nil && len(res.Machines) == 5
	})

	// What it would erase, shown before asking: each device, machine and
	// agent once (the Mac's own machine is the Mac), with who is online.
	eventually(t, 30*time.Second, "the Mac lists mac2 among the devices", func() bool {
		res, err := w.app.client.ListDevices(ctx, &localv1.ListDevicesRequest{})
		return err == nil && len(res.Devices) == 2
	})
	preview, err := w.app.client.EraseStatus(ctx, &localv1.EraseStatusRequest{})
	if err != nil {
		t.Fatal(err)
	}
	var shown []string
	for _, it := range preview.Items {
		shown = append(shown, fmt.Sprintf("%s/%s this=%v online=%v done=%v", it.Kind, it.Name, it.ThisDevice, it.Online, it.Done))
	}
	if want := []string{"device/test-mac this=true online=false done=false", "device/mac2 this=false online=false done=false",
		"machine/mac3 this=false online=true done=false", "machine/vps-late this=false online=false done=false",
		"machine/vps-off this=false online=false done=false", "machine/vps-on this=false online=true done=false",
		"agent/bot this=false online=false done=false", "agent/late this=false online=false done=false"}; !slices.Equal(shown, want) {
		t.Fatalf("preview:\n%s", strings.Join(shown, "\n"))
	}

	// Erase everything, from the Mac: the server signs the order.
	before := w.signs()
	erase, err := w.app.client.EraseEverything(ctx, &localv1.EraseEverythingRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if got := w.signs() - before; got != 1 {
		t.Fatalf("erasing signed %d times", got)
	}
	// Asked again (a second click) while erasing: refused, nothing signed.
	if _, err := w.app.client.EraseEverything(ctx, &localv1.EraseEverythingRequest{}); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("erasing twice: %v", err)
	}

	// The agent learns it at its next command, and forgets its pairing.
	r = w.cli(30*time.Second, "project", "list")
	if r.code != 3 || !strings.Contains(r.stderr, "the owner erased everything") {
		t.Fatalf("the agent after the erase: %+v", r)
	}
	if _, err := os.Stat(filepath.Join(w.tmp, "agent", "state.json")); !os.IsNotExist(err) {
		t.Fatalf("the agent kept its pairing: %v", err)
	}

	// The Mac shows each one erasing; vps-off stays to do.
	status := func() map[string]*localv1.EraseItem {
		res, err := w.app.client.EraseStatus(ctx, &localv1.EraseStatusRequest{EraseId: erase.EraseId})
		if err != nil {
			t.Fatal(err)
		}
		items := map[string]*localv1.EraseItem{}
		for _, it := range res.Items {
			items[it.Kind+"/"+it.Name] = it
		}
		return items
	}
	eventually(t, time.Minute, "the others erasing", func() bool {
		items := status()
		for _, k := range []string{"device/mac2", "machine/vps-on", "machine/mac3", "agent/bot"} {
			if items[k] == nil || !items[k].Done {
				return false
			}
		}
		return true
	})
	items := status()
	if len(items) != 8 || !items["device/test-mac"].ThisDevice || items["device/test-mac"].Done || items["machine/vps-off"].Done {
		t.Fatalf("erase items: %v", items)
	}
	if c := vpsOn.wait(t, 30*time.Second); c != 0 {
		t.Fatalf("vps-on exited %d: %s", c, vpsOn.output())
	}
	machineErased(onData)
	if b, err := os.ReadFile(appFile); err != nil || string(b) != "another owner" {
		t.Fatalf("erasing the machine erased the app's owner too: %q %v", b, err)
	}

	// The Mac erases itself last, and opens empty, saying what was done.
	if _, err := w.app.client.FinishErase(ctx, &localv1.FinishEraseRequest{}); err != nil {
		t.Fatal(err)
	}
	st, err := w.app.client.GetStatus(ctx, &localv1.GetStatusRequest{})
	if err != nil || st.Initialized || st.Erased == nil || st.Erased.EraseId != erase.EraseId || !slices.Equal(st.Erased.Pending, []string{"vps-late", "vps-off", "late"}) ||
		len(st.Erased.KeptWorktrees) != 1 {
		t.Fatalf("the Mac after erasing: %v %v", st, err)
	}
	kept := st.Erased.KeptWorktrees[0]
	if b, err := os.ReadFile(filepath.Join(kept, "unsaved.txt")); err != nil || string(b) != "work in progress\n" {
		t.Fatalf("the kept worktree %s: %q %v", kept, b, err)
	}
	if _, err := os.Stat(cleanWT); !os.IsNotExist(err) {
		t.Fatalf("the clean worktree is left: %v", err)
	}
	if _, err := os.Stat(dirtyWT); !os.IsNotExist(err) {
		t.Fatalf("the dirty worktree was not moved out: %v", err)
	}
	if out := gitOut(t, kept, "status", "--porcelain"); !strings.Contains(out, "unsaved.txt") {
		t.Fatalf("the kept worktree is no longer a worktree: %q", out)
	}
	if _, err := w.app.client.ListMachines(ctx, &localv1.ListMachinesRequest{}); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("listing machines after erasing: %v", err)
	}
	if _, err := os.Stat(herMac); err != nil {
		t.Fatalf("a her of the Mac's own, where Her was never on: %v", err)
	}
	if _, err := os.Stat(herVPS); err != nil {
		t.Fatalf("a her of the server's own, where Her was never on: %v", err)
	}

	// The other device and the machine-only Mac erased themselves.
	eventually(t, 30*time.Second, "mac2 erased", func() bool {
		st, err := app2.client.GetStatus(ctx, &localv1.GetStatusRequest{})
		return err == nil && !st.Initialized && st.Erased != nil && st.Erased.EraseId == erase.EraseId && st.Erased.By == "test-mac"
	})
	eventually(t, 30*time.Second, "mac3 erased", func() bool {
		st, err := mac3.GetStatus(ctx, &localv1.GetStatusRequest{})
		return err == nil && !st.MachineOnly && !st.Initialized && st.Erased != nil
	})

	// vps-off, started again, learns it then.
	vpsOff = runVPS("vps-off", offData)
	if c := vpsOff.wait(t, time.Minute); c != 0 {
		t.Fatalf("vps-off exited %d: %s", c, vpsOff.output())
	}
	if !strings.Contains(vpsOff.output(), "The owner erased everything") {
		t.Fatalf("vps-off said: %s", vpsOff.output())
	}
	machineErased(offData)

	// A cold start: the server can be set up again, and the Mac with it.
	claim := strings.TrimSpace(run(t, nil, 10*time.Second, filepath.Join(w.bin, "aide-relay"), "setup-code", "--data-dir", filepath.Join(w.tmp, "relay")).stdout)
	setup, err := w.app.client.Setup(ctx, &localv1.SetupRequest{RelayUrl: home.RelayUrl, SetupCode: claim, Account: testAccount, Password: testPassword, Label: "test-mac"})
	if err != nil {
		t.Fatalf("setting up again: %v", err)
	}
	if setup.OwnerId == home.OwnerId {
		t.Fatal("set up again as the erased owner")
	}
	if st, _ := w.app.client.GetStatus(ctx, &localv1.GetStatusRequest{}); !st.Initialized || st.Erased != nil {
		t.Fatalf("status once set up again: %v", st)
	}

	// Those the erase never reached log in to the new owner as they are:
	// their old owner is gone from the server, nothing to log out of first.
	gone := "is gone from its server (it erased everything)"
	login := start(t, "login late again", append(slices.Clone(lateAgent), loginEnv()...), filepath.Join(w.bin, "aide"), "auth", "login", home.RelayUrl, "--name", "late")
	if c := login.wait(t, 30*time.Second); c != 0 || !strings.Contains(login.output(), gone) {
		t.Fatalf("the late agent logging in again: %d\n%s", c, login.output())
	}
	pair := start(t, "pair vps-late again", append(slices.Clone(env), loginEnv()...), daemon, "login", home.RelayUrl, "--name", "vps-late", "--no-service", "--data-dir", lateData)
	if c := pair.wait(t, 30*time.Second); c != 0 || !strings.Contains(pair.output(), gone) {
		t.Fatalf("vps-late logging in again: %d\n%s", c, pair.output())
	}
}

// TestEraseThisMachine: `aided erase` on a server erases its machine
// only: not while it runs, then unpaired (its owner's devices stop listing
// it) and its data gone. On one of the owner's devices it says where to
// erase everything instead.
func TestEraseThisMachine(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e")
	}
	env := []string{"AIDE_FAKE_PROVIDERS=1"}
	w := newOwnerMac(t, env, &localv1.GrantSpec{})
	ctx := context.Background()
	daemon := filepath.Join(w.bin, "aided")
	data := filepath.Join(w.tmp, "vps-1")
	pair := start(t, "pair", append(slices.Clone(env), loginEnv()...), daemon, "login", w.addr, "--name", "vps-1", "--no-service", "--data-dir", data)
	if c := pair.wait(t, 30*time.Second); c != 0 {
		t.Fatalf("pair: %s", pair.output())
	}
	vps := start(t, "vps-1", env, daemon, "run", "--data-dir", data)
	w.logs = append(w.logs, vps)
	listed := func() bool {
		res, err := w.app.client.ListMachines(ctx, &localv1.ListMachinesRequest{})
		return err == nil && slices.ContainsFunc(res.Machines, func(m *localv1.Machine) bool { return m.MachineName == "vps-1" })
	}
	eventually(t, 30*time.Second, "vps-1 online", listed)

	if r := run(t, nil, 30*time.Second, daemon, "erase", "--yes", "--data-dir", data); r.code == 0 || !strings.Contains(r.stderr, "stop `aided run` here first") {
		t.Fatalf("erasing a running machine: %+v", r)
	}
	vps.stop()
	r := run(t, nil, 60*time.Second, daemon, "erase", "--yes", "--data-dir", data)
	if r.code != 0 || !strings.Contains(r.stdout, "“vps-1”") || !strings.Contains(r.stdout, "✓ Aide on this computer is erased") {
		t.Fatalf("erase: %+v", r)
	}
	if _, err := os.Stat(filepath.Join(data, "machine")); !os.IsNotExist(err) {
		t.Fatalf("the machine kept: %v", err)
	}
	eventually(t, 30*time.Second, "vps-1 gone from the owner's machines", func() bool { return !listed() })

	// One of the owner's devices (this Mac's owner role) erases everything instead, in the app.
	if r := run(t, nil, 30*time.Second, daemon, "erase", "--yes", "--data-dir", filepath.Join(w.tmp, "owner-mac")); r.code == 0 ||
		!strings.Contains(r.stderr, "erase Aide on all your devices in the app") {
		t.Fatalf("erase on one of the owner's devices: %+v", r)
	}
}
