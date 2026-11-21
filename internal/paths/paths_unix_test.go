//go:build unix

package paths

import (
	"strings"
	"testing"
)

// Sockets have a short path limit: a long data directory's control socket
// moves to the temp folder.
func TestControlSocketIsPerMachine(t *testing.T) {
	long := "/tmp/" + strings.Repeat("x", 120)
	a, b := Control(long+"/a"), Control(long+"/b")
	if a == b || len(a) > 100 {
		t.Fatalf("%s, %s", a, b)
	}
	if got := Control("/home/dev/.local/share/aide/machine"); got != "/home/dev/.local/share/aide/machine/control.sock" {
		t.Fatal(got)
	}
}

// The CLI's link is next to the home's lock, and under the limit whatever
// the home, one per home.
func TestLinkSocket(t *testing.T) {
	long := "/tmp/" + strings.Repeat("x", 120)
	a, b := Link(long+"/a"), Link(long+"/b")
	if a == b || len(a) > 100 || strings.HasPrefix(a, long) {
		t.Fatalf("%s, %s", a, b)
	}
	if got := Link("/home/dev/.config/aide"); got != "/home/dev/.config/aide/link/link.sock" {
		t.Fatal(got)
	}
}
