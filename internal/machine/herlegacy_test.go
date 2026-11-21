package machine

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"connectrpc.com/connect"

	"github.com/samir2015dzh/aide/internal/provider"
)

// openPlainMachine opens a machine with no coding agents in a data
// directory of its own.
func openPlainMachine(t *testing.T) *Machine {
	t.Helper()
	n, err := Open(Config{DataDir: t.TempDir(), Providers: map[string]provider.Provider{}})
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// A machine says it has no Her in words an app from before reads as "can't
// run her here", never as a machine to update.
func TestHerStatusSaysRemoved(t *testing.T) {
	t.Setenv("HER_HOME", filepath.Join(t.TempDir(), "her-home"))
	n := openPlainMachine(t)
	defer n.Close()
	st := n.Status().Her
	if st == nil || st.Unsupported != herRemoved || st.Enabled || st.Running || st.Data {
		t.Fatalf("Her's status = %v, want only unsupported=%q", st, herRemoved)
	}
}

// Her's data from before is said to be there, and goes when an app from
// before asks for it; turning her on is refused.
func TestHerDataFromBefore(t *testing.T) {
	home := filepath.Join(t.TempDir(), "her-home")
	t.Setenv("HER_HOME", home)
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(home, herMark), nil, 0o600)
	os.WriteFile(filepath.Join(home, "config.toml"), []byte("x = 1\n"), 0o600)
	dir := t.TempDir()
	open := func() *Machine {
		n, err := Open(Config{DataDir: dir, Providers: map[string]provider.Provider{}})
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	n := open()
	n.st.Set(kHer, []byte("1")) // Her was on here
	n.Close()

	n = open()
	defer n.Close()
	if d := n.herDataKept(); !d.Kept || d.Home != home {
		t.Fatalf("her data = %+v, want kept in %s", d, home)
	}
	if _, err := n.setHer(true, false); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("turning Her on = %v, want failed_precondition", err)
	}
	if msg, err := n.setHer(false, false); err != nil || msg != HerOff {
		t.Fatalf("turning Her off = %q, %v", msg, err)
	}
	if _, err := os.Stat(home); err != nil {
		t.Fatalf("her home went without being asked: %v", err)
	}
	if msg, err := n.setHer(false, true); err != nil || msg != HerErased {
		t.Fatalf("erasing = %q, %v", msg, err)
	}
	if _, err := os.Stat(home); !os.IsNotExist(err) {
		t.Fatalf("her home is still there: %v", err)
	}
	if n.Status().Her.Data {
		t.Fatal("status still says her data is kept")
	}
}

// A home that is not hers alone is never deleted.
func TestHerHomeNotHersStays(t *testing.T) {
	home := filepath.Join(t.TempDir(), "shared")
	t.Setenv("HER_HOME", home)
	os.MkdirAll(home, 0o700)
	os.WriteFile(filepath.Join(home, "someone-elses.txt"), []byte("keep"), 0o600)
	n := openPlainMachine(t)
	defer n.Close()
	if msg, err := n.setHer(false, true); err != nil || msg == HerErased {
		t.Fatalf("erasing a home that is not hers = %q, %v", msg, err)
	}
	if _, err := os.Stat(filepath.Join(home, "someone-elses.txt")); err != nil {
		t.Fatalf("a file that was not hers went: %v", err)
	}
}

// The keys a machine kept for Her as an agent, and her grant, go when it
// starts: nobody can use a grant of hers that still stands.
func TestHerKeysForgotten(t *testing.T) {
	t.Setenv("HER_HOME", filepath.Join(t.TempDir(), "her-home"))
	dir := t.TempDir()
	keys := filepath.Join(dir, "keys")
	os.MkdirAll(keys, 0o700)
	for _, name := range []string{"her.key", "her-inbox.key"} {
		os.WriteFile(filepath.Join(keys, name), []byte("k"), 0o600)
	}
	n, err := Open(Config{DataDir: dir, Providers: map[string]provider.Provider{}})
	if err != nil {
		t.Fatal(err)
	}
	n.st.Set(kHerGrant, []byte("signed"))
	n.Close()
	os.WriteFile(filepath.Join(keys, "her.key"), []byte("k"), 0o600)
	if n, err = Open(Config{DataDir: dir, Providers: map[string]provider.Provider{}}); err != nil {
		t.Fatal(err)
	}
	defer n.Close()
	for _, name := range []string{"her.key", "her-inbox.key"} {
		if _, err := os.Stat(filepath.Join(keys, name)); !os.IsNotExist(err) {
			t.Fatalf("%s is still there: %v", name, err)
		}
	}
	if g, _ := n.st.Get(kHerGrant); len(g) != 0 {
		t.Fatalf("her grant is still kept: %q", g)
	}
}

// waitFor waits for cond, at most half a minute.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(50 * time.Millisecond)
	}
}
