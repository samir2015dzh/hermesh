package machine

import (
	"context"
	"sync"
	"testing"
	"time"

	machinev1 "github.com/samir2015dzh/aide/gen/go/aide/machine/v1"
	"github.com/samir2015dzh/aide/internal/provider"
	"github.com/samir2015dzh/aide/internal/provider/claude"
)

// fakeUsage returns queued readings, counting the calls.
type fakeUsage struct {
	mu    sync.Mutex
	out   []claude.Usage
	calls int
}

func (f *fakeUsage) Read(context.Context) claude.Usage {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if len(f.out) == 0 {
		return claude.Usage{}
	}
	u := f.out[0]
	if len(f.out) > 1 {
		f.out = f.out[1:]
	}
	return u
}

func (f *fakeUsage) count() int { f.mu.Lock(); defer f.mu.Unlock(); return f.calls }

func openTestMachine(t *testing.T) *Machine {
	t.Helper()
	n, err := Open(Config{DataDir: t.TempDir(), Providers: map[string]provider.Provider{"claude": &claude.Provider{Binary: "/nonexistent/claude"}}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { n.Close() })
	// Check agents once so the Claude row is listed (it is listed whether or
	// not Claude Code is found).
	n.Discover(context.Background())
	return n
}

// claudeAgent returns the status row for Claude Code.
func claudeAgent(st *machinev1.MachineStatus) *machinev1.AgentStatus {
	for _, a := range st.Agents {
		if a.Name == "claude" {
			return a
		}
	}
	return nil
}

func waitUsage(t *testing.T, n *Machine) *machinev1.ClaudeUsage {
	t.Helper()
	for i := 0; i < 200; i++ {
		if u := claudeAgent(n.Status()).GetUsage(); u != nil {
			return u
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("usage never attached to the Claude agent")
	return nil
}

func TestStatusAttachesUsage(t *testing.T) {
	n := openTestMachine(t)
	f := &fakeUsage{out: []claude.Usage{{
		Windows:   []claude.Window{{Key: "five_hour", Utilization: 20, ResetsAt: time.Now().Add(time.Hour)}, {Key: "seven_day", Utilization: 59}},
		FetchedAt: time.Now(),
	}}}
	n.usageSrc = f

	// First Status kicks a background fetch; the reading follows.
	u := waitUsage(t, n)
	if len(u.Windows) != 2 || u.Windows[0].Key != "five_hour" {
		t.Fatalf("windows = %+v", u.Windows)
	}
	if u.Unavailable != "" {
		t.Fatalf("unavailable = %q", u.Unavailable)
	}

	// Within the TTL no further fetch is made, however often status is read.
	for i := 0; i < 5; i++ {
		n.Status()
	}
	if c := f.count(); c != 1 {
		t.Fatalf("usage fetched %d times within TTL, want 1", c)
	}
}

func TestUsageStaleFallback(t *testing.T) {
	n := openTestMachine(t)
	good := claude.Usage{Windows: []claude.Window{{Key: "five_hour", Utilization: 42}}, FetchedAt: time.Now()}
	f := &fakeUsage{out: []claude.Usage{good, {Unavailable: "api_error", FetchedAt: time.Now()}}}
	n.usageSrc = f

	first := waitUsage(t, n)
	if first.Stale || len(first.Windows) != 1 {
		t.Fatalf("first reading = %+v", first)
	}

	// Force the TTL to lapse and fetch again: the endpoint now fails, so the
	// last good windows are kept and marked stale.
	n.mu.Lock()
	n.usageAt = time.Now().Add(-2 * usageEvery)
	n.mu.Unlock()
	n.Status() // kicks the second fetch
	for i := 0; i < 200; i++ {
		u := claudeAgent(n.Status()).GetUsage()
		if u != nil && u.Stale {
			if len(u.Windows) != 1 || u.Windows[0].Utilization != 42 {
				t.Fatalf("stale windows = %+v", u.Windows)
			}
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("stale fallback never applied")
}

func TestUsageDisabledByDefault(t *testing.T) {
	// No usageSrc: status carries no usage and nothing is fetched.
	n := openTestMachine(t)
	for i := 0; i < 3; i++ {
		if u := claudeAgent(n.Status()).GetUsage(); u != nil {
			t.Fatalf("usage attached with no source: %+v", u)
		}
	}
}
