package e2e

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	localv1 "github.com/samir2015dzh/aide/gen/go/aide/local/v1"
	"github.com/samir2015dzh/aide/gen/go/aide/local/v1/localv1connect"
	"github.com/samir2015dzh/aide/internal/flock"
	"github.com/samir2015dzh/aide/internal/localipc"
	"github.com/samir2015dzh/aide/internal/paths"
	"github.com/samir2015dzh/aide/pkg/identity"
	"google.golang.org/protobuf/proto"
)

var (
	buildOnce sync.Once
	binDir    string
	buildErr  error
)

func repoRoot() string {
	wd, _ := os.Getwd()
	return filepath.Dir(wd)
}

// binaries builds aide-relay, aided and aide once per test run.
func binaries(t *testing.T) string {
	t.Helper()
	buildOnce.Do(func() {
		binDir, buildErr = os.MkdirTemp("", "aide-e2e-bin")
		if buildErr != nil {
			return
		}
		for _, pkg := range []string{"./cmd/aide-relay", "./cmd/aided", "./cmd/aide", "./internal/provider/acp/fakeacp", "./bench/fakecodex"} {
			cmd := filepath.Base(pkg)
			args := []string{"build", "-o", filepath.Join(binDir, cmd+exe)}
			// AIDE_E2E_RACE: 1 for every binary, or the ones named, comma-separated
			// (a pull request races what its changes reach: scripts/ci_select.py).
			if r := os.Getenv("AIDE_E2E_RACE"); r == "1" || slices.Contains(strings.Split(r, ","), cmd) {
				args = append(args, "-race") // the binaries exit 66 on a data race
			}
			out, err := exec.Command("go", append(args, pkg)...).CombinedOutput()
			if err != nil {
				buildErr = fmt.Errorf("build %s: %v\n%s", cmd, err, out)
				return
			}
		}
	})
	if buildErr != nil {
		t.Fatal(buildErr)
	}
	return binDir
}

func init() {
	// go build must run from the module root.
	if wd, err := os.Getwd(); err == nil && filepath.Base(wd) == "e2e" {
		os.Chdir(filepath.Dir(wd))
	}
}

func freePort(t *testing.T) int {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

// proc is a background process with captured output.
type proc struct {
	name  string
	cmd   *exec.Cmd
	mu    sync.Mutex
	out   bytes.Buffer
	lines chan string
	done  chan struct{}
}

func start(t *testing.T, name string, env []string, bin string, args ...string) *proc {
	t.Helper()
	endLinks(t, env)
	cmd := exec.Command(bin, args...)
	cmd.Env = append(os.Environ(), env...)
	ownGroup(cmd)
	pr, pw := io.Pipe()
	cmd.Stdout, cmd.Stderr = pw, pw
	p := &proc{name: name, cmd: cmd, lines: make(chan string, 1024), done: make(chan struct{})}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go func() {
		sc := bufio.NewScanner(pr)
		for sc.Scan() {
			l := sc.Text()
			p.mu.Lock()
			p.out.WriteString(l + "\n")
			p.mu.Unlock()
			select {
			case p.lines <- l:
			default:
			}
		}
	}()
	go func() {
		cmd.Wait()
		pw.Close()
		close(p.done)
	}()
	t.Cleanup(func() { p.kill() })
	return p
}

func (p *proc) kill() {
	select {
	case <-p.done:
		return
	default:
	}
	killGroup(p.cmd.Process.Pid)
	<-p.done
}

func (p *proc) stop() {
	select {
	case <-p.done:
		return
	default:
	}
	stopGroup(p.cmd.Process.Pid)
	select {
	case <-p.done:
	case <-time.After(10 * time.Second):
		p.kill()
	}
}

func (p *proc) output() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.out.String()
}

func (p *proc) wait(t *testing.T, timeout time.Duration) int {
	t.Helper()
	select {
	case <-p.done:
		return p.cmd.ProcessState.ExitCode()
	case <-time.After(timeout):
		t.Fatalf("%s did not exit:\n%s", p.name, p.output())
	}
	return -1
}

// result of a one-shot CLI command.
type result struct {
	stdout, stderr string
	code           int
}

func run(t *testing.T, env []string, timeout time.Duration, bin string, args ...string) result {
	t.Helper()
	endLinks(t, env)
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Env = append(os.Environ(), env...)
	var so, se bytes.Buffer
	cmd.Stdout, cmd.Stderr = &so, &se
	err := cmd.Run()
	r := result{stdout: so.String(), stderr: se.String()}
	if cmd.ProcessState != nil {
		r.code = cmd.ProcessState.ExitCode()
	}
	if ctx.Err() != nil {
		t.Fatalf("%s %v timed out\nstdout:%s\nstderr:%s", filepath.Base(bin), args, r.stdout, r.stderr)
	}
	_ = err
	return r
}

// The owner's account and password in the tests (docs/accounts.md).
const testAccount, testPassword = "owner", "correct horse battery"

// loginEnv is what a command logs in with: the test account and password.
func loginEnv() []string {
	return []string{"AIDE_ACCOUNT=" + testAccount, "AIDE_PASSWORD=" + testPassword}
}

// testApp is the desktop app as the tests drive it: a client of the
// bundled aided over its local socket.
type testApp struct {
	t      *testing.T
	client localv1connect.LocalServiceClient
	cancel context.CancelFunc
}

func newTestApp(t *testing.T, sock string) *testApp {
	return &testApp{t: t, client: localClient(t, sock), cancel: func() {}}
}

// localClient is a client of aided's local API on sock, once it answers.
func localClient(t *testing.T, sock string) localv1connect.LocalServiceClient {
	hc := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return localipc.Dial(ctx, sock)
	}}}
	c := localv1connect.NewLocalServiceClient(hc, "http://local")
	deadline := time.Now().Add(20 * time.Second)
	for {
		if _, err := c.GetStatus(context.Background(), &localv1.GetStatusRequest{}); err == nil {
			return c
		} else if time.Now().After(deadline) {
			t.Fatalf("local API not reachable: %v", err)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func rootPub(k *identity.SoftwareKey) []byte { return identity.MarshalPublicKey(k.Public()) }

func gitRepo(t *testing.T, files map[string]string) string {
	dir := t.TempDir()
	dir, _ = filepath.EvalSymlinks(dir)
	gitRepoAt(t, dir, files)
	return dir
}

// gitRepoAt makes dir a git repository on main with files committed.
func gitRepoAt(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	os.MkdirAll(dir, 0o755)
	for name, content := range files {
		os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644)
	}
	env := append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"add", "-A"}, {"commit", "-q", "-m", "init"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir, cmd.Env = dir, env
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
}

func eventually(t *testing.T, timeout time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

var _ = proto.Equal
var _ = strings.Contains

// withoutInboxNote is stderr without the line saying messages wait in the
// agent's inbox (docs/agent-messages.md): a hint, not a warning.
func withoutInboxNote(stderr string) string {
	var out []string
	for _, l := range strings.Split(stderr, "\n") {
		if !strings.Contains(l, "wait in your inbox: aide message list") {
			out = append(out, l)
		}
	}
	return strings.Join(out, "\n")
}

// endLinks has the agent's link (docs/cli.md), which a command in env may
// start apart from itself, end with the test: it outlives the command, and
// writing in its home while the test's temp folder is removed it would fail
// the test. Once per home and test; the cleanup runs before that removal,
// registered after the folder was made.
func endLinks(t *testing.T, env []string) {
	var home string
	for _, e := range env {
		if v, ok := strings.CutPrefix(e, "AIDE_HOME="); ok {
			home = v
		}
	}
	if home == "" {
		return
	}
	if _, loaded := linkHomes.LoadOrStore(t.Name()+"\x00"+home, true); loaded {
		return
	}
	t.Cleanup(func() {
		dir := paths.LinkLock(home)
		for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
			b, _ := os.ReadFile(filepath.Join(dir, "lock"))
			pid, _ := strconv.Atoi(strings.TrimSpace(string(b)))
			// Its lock held: a link runs, the one whose pid the lock says.
			l, err := flock.Acquire(dir)
			if err == nil {
				l.Release()
				return
			}
			if p, err := os.FindProcess(pid); pid > 0 && err == nil {
				p.Kill()
			}
		}
	})
}

var linkHomes sync.Map
