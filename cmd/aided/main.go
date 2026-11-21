// Command aided is the Aide daemon: it runs an execution machine (the machine role)
// and, when bundled with Aide.app, the owner role behind the app.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"syscall"
	"time"
	_ "time/tzdata" // Windows has no zoneinfo to read: the days and hours of QueryWork need it

	"github.com/spf13/cobra"

	localv1 "github.com/samir2015dzh/aide/gen/go/aide/local/v1"
	"github.com/samir2015dzh/aide/gen/go/aide/local/v1/localv1connect"
	relayv1 "github.com/samir2015dzh/aide/gen/go/aide/relay/v1"
	"github.com/samir2015dzh/aide/gen/go/aide/relay/v1/relayv1connect"
	"github.com/samir2015dzh/aide/internal/brandenv"
	"github.com/samir2015dzh/aide/internal/connaddr"
	"github.com/samir2015dzh/aide/internal/localipc"
	"github.com/samir2015dzh/aide/internal/machine"
	"github.com/samir2015dzh/aide/internal/owner"
	"github.com/samir2015dzh/aide/internal/paths"
	"github.com/samir2015dzh/aide/internal/provider"
	"github.com/samir2015dzh/aide/internal/provider/acp"
	"github.com/samir2015dzh/aide/internal/provider/claude"
	"github.com/samir2015dzh/aide/internal/provider/codex"
	"github.com/samir2015dzh/aide/internal/provider/fake"
	"github.com/samir2015dzh/aide/internal/resident"
	"github.com/samir2015dzh/aide/internal/service"
	"github.com/samir2015dzh/aide/internal/shellenv"
	"github.com/samir2015dzh/aide/internal/spawn"
	"github.com/samir2015dzh/aide/internal/update"
	"github.com/samir2015dzh/aide/pkg/metrics"
	"github.com/samir2015dzh/aide/pkg/rlimit"
)

var logOut io.Writer = os.Stderr

// spawnHelpers is how many spawn helper processes run; each serves one
// spawn at a time, and four queued at 5000 sessions (bench/README.md).
var spawnHelpers = max(2, runtime.NumCPU()/2)

func logger(verbose bool) *slog.Logger {
	lvl := slog.LevelInfo
	if verbose {
		lvl = slog.LevelDebug
	}
	return slog.New(slog.NewTextHandler(logOut, &slog.HandlerOptions{Level: lvl}))
}

// serveUntilStopped runs run in the background and serve until ctx ends,
// or serve fails and stop ends it, then waits for run to return. The owner
// role's run stops this computer's machine.
func serveUntilStopped(ctx context.Context, stop context.CancelFunc, run func(context.Context), serve func(context.Context) error) error {
	done := make(chan struct{})
	go func() {
		defer close(done)
		run(ctx)
	}()
	err := serve(ctx)
	stop()
	<-done
	return err
}

// openLogFile sends logs to <dataDir>/logs/aided.log as well (launchd
// and SMAppService give the daemon no terminal).
func openLogFile(dataDir string) {
	dir := filepath.Join(dataDir, "logs")
	os.MkdirAll(dir, 0o700)
	path := filepath.Join(dir, "aided.log")
	if st, err := os.Stat(path); err == nil && st.Size() > 20<<20 {
		os.Rename(path, path+".1")
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err == nil {
		logOut = io.MultiWriter(os.Stderr, f)
	}
}

// logHint is how to read the log in this system's terminal: following it,
// or its last lines.
func logHint(path string, follow bool) string {
	switch {
	case runtime.GOOS == "windows" && follow:
		return `Get-Content -Wait "` + path + `"`
	case runtime.GOOS == "windows":
		return `Get-Content -Tail 50 "` + path + `"`
	case follow:
		return "tail -f " + path
	}
	return "tail " + path
}

func main() {
	if err := rootCommand().Execute(); err != nil {
		os.Exit(1)
	}
}

func rootCommand() *cobra.Command {
	var dataDir string
	var verbose bool
	root := &cobra.Command{Use: "aided", Short: "Aide on a machine: runs coding agent sessions for your agents", SilenceUsage: true,
		Long: `aided runs on each machine where coding agents (Claude Code, Codex) work
for your agents. It runs as the user you code as, in the background.

First time, the install script runs "aided login" for you. Afterwards:
  aided doctor                   check everything, and what to fix
  aided project add ~/code/app   let agents work in a folder
  aided service status           is it running, and does it start at boot

A computer that runs no coding agents can approve instead: aided device.`}
	root.PersistentFlags().StringVar(&dataDir, "data-dir", paths.DataDir(), "data directory")
	root.PersistentFlags().BoolVarP(&verbose, "verbose", "v", false, "debug logging")

	var debugAddr string
	var maxSessions int
	var idleProcessTTL time.Duration
	var idleTimeout, toolTimeout, turnTimeout, retryDelay time.Duration
	var runFlagSet func(name string) bool // whether a `run` flag was given
	var workspaceMode string
	machineConfig := func() machine.Config {
		lim := machine.DefaultLimits()
		if maxSessions > 0 {
			lim.MaxConcurrent = maxSessions
		}
		if idleProcessTTL > 0 {
			lim.IdleProcessTTL = idleProcessTTL
		}
		for _, o := range []struct {
			flag string
			v    time.Duration
			dst  *time.Duration
		}{{"idle-timeout", idleTimeout, &lim.IdleTimeout}, {"tool-timeout", toolTimeout, &lim.ToolTimeout}, {"turn-timeout", turnTimeout, &lim.TurnTimeout},
			{"retry-delay", retryDelay, &lim.RetryDelay}} {
			if runFlagSet != nil && runFlagSet(o.flag) {
				*o.dst = o.v
			}
		}
		return machine.Config{DataDir: filepath.Join(dataDir, "machine"), Logger: logger(verbose), Providers: providers(), Limits: lim, WorkspaceMode: workspaceMode,
			ClaudeUsage: os.Getenv("AIDE_NO_CLAUDE_USAGE") != "1"}
	}
	openMachine := func() (*machine.Machine, error) { return machine.Open(machineConfig()) }

	var ownerMode, noMachine, supervise bool
	var socket string
	run := &cobra.Command{
		Use:   "run",
		Short: "Run the daemon in the foreground (used by launchd/systemd and the Aide app)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if supervise { // the Windows service: the daemon is a child of this process
				exe, err := os.Executable()
				if err != nil {
					return err
				}
				var args []string
				for _, a := range os.Args[1:] {
					if a != "--supervise" {
						args = append(args, a)
					}
				}
				return service.Supervise(dataDir, exe, args)
			}
			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			openLogFile(dataDir)
			log := logger(verbose)
			resident.SetMemoryLimit(log)
			if n := rlimit.RaiseNoFile(); n > 0 { // Windows has no such limit
				log.Info("open file limit", "soft", n)
			}
			switch reloading, err := service.ReloadOldJob(dataDir); {
			case err != nil:
				log.Warn("updating the service's launchd job", "err", err)
			case reloading:
				log.Info("launchd is loading the service's updated job")
				select {
				case <-ctx.Done():
					return nil
				case <-time.After(time.Minute):
					log.Warn("launchd did not load the service's updated job; it applies at the next login")
				}
			}
			if exe, err := os.Executable(); err == nil && spawn.Supported {
				// Spawning from small helpers keeps agent start cost flat
				// as this process accumulates descriptors (see spawn.Helper).
				var hs []*spawn.Helper
				for i := 0; i < spawnHelpers; i++ {
					h, err := spawn.StartHelper(exe, "spawn-helper")
					if err != nil {
						log.Warn("spawn helper unavailable", "err", err)
						break
					}
					hs = append(hs, h)
					defer h.Close()
				}
				provider.UseSpawnHelpers(hs...)
			}
			if debugAddr != "" {
				go func() { log.Warn("debug server stopped", "err", metrics.Serve(debugAddr)) }()
			}
			// The background updater (docs/machine-updates.md): after an
			// update aided stops, and its service starts the new program.
			autoUpdate := func(busy func() int) {
				if !service.Supervised(dataDir) {
					return
				}
				if _, err := update.Dir(); err != nil { // the Windows app's, which updates it
					return
				}
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				go func() {
					if update.Background(ctx, dataDir, machine.Version, busy, log) {
						cancel()
					}
				}()
			}
			// Power saving while nothing runs and no window looks (Windows, docs/resident.md §2).
			savePower := func(m func() *machine.Machine) {
				resident.SetGroupPids(provider.GroupPids)
				saver := resident.NewSaver(log, func() int {
					if n := m(); n != nil {
						return n.Busy()
					}
					return 0
				}, func() int { return 0 }) // no process of Her's to throttle with aided: Aide no longer has her
				go saver.Run(ctx)
			}
			if ownerMode {
				o, err := owner.Open(owner.Config{DataDir: filepath.Join(dataDir, "owner"), Logger: log, MachineConfig: machineConfig(), NoLocalMachine: noMachine})
				if err != nil {
					return err
				}
				savePower(o.LocalMachine)
				if socket == "" {
					socket = paths.LocalAPI(dataDir)
				}
				if noMachine { // a Linux trusted device
					autoUpdate(func() int { return 0 })
				}
				log.Info("serving local API", "socket", socket)
				return serveUntilStopped(ctx, stop, o.Run, func(ctx context.Context) error { return o.ServeLocal(ctx, socket) })
			}
			cfg := machineConfig()
			cfg.SelfErase = true
			n, err := machine.Open(cfg)
			if err != nil {
				return err
			}
			autoUpdate(n.Busy)
			savePower(func() *machine.Machine { return n })
			err = n.Run(ctx)
			n.Close()
			if errors.Is(err, machine.ErrErased) {
				return eraseMachine(dataDir, cfg)
			}
			return err
		},
	}
	run.Flags().BoolVar(&ownerMode, "owner", false, "run the owner role for the Aide app (serves the local API)")
	run.Flags().StringVar(&socket, "socket", "", "local API socket path (owner role)")
	run.Flags().BoolVar(&noMachine, "no-machine", false, "with --owner: never run a machine here")
	run.Flags().StringVar(&debugAddr, "debug-addr", brandenv.Get("DEBUG_ADDR"), "serve /metrics and /debug/pprof on this loopback address")
	run.Flags().BoolVar(&supervise, "supervise", false, "run the daemon as a child, and start it again whenever it exits but for a clean stop (Windows)")
	run.Flags().MarkHidden("supervise")
	run.Flags().IntVar(&maxSessions, "max-sessions", 0, "maximum concurrently running sessions; more wait queued (default: no limit)")
	run.Flags().StringVar(&workspaceMode, "workspace", "git", "git (worktree per session) or none (benchmarking only)")
	run.Flags().MarkHidden("workspace")
	run.Flags().DurationVar(&idleProcessTTL, "idle-process-ttl", 0, "stop an idle session's agent process after this long (default 10m)")
	run.Flags().DurationVar(&idleTimeout, "idle-timeout", 0, "stop a turn whose agent writes nothing for this long while no tool runs (default 15m; 0: never)")
	run.Flags().DurationVar(&toolTimeout, "tool-timeout", 0, "stop a turn whose agent writes nothing for this long while a tool runs (default 2h; 0: never)")
	run.Flags().DurationVar(&turnTimeout, "turn-timeout", 0, "stop a turn that runs longer than this even while it makes progress (default: no limit)")
	run.Flags().DurationVar(&retryDelay, "retry-delay", 0, "a turn stopped by a rate limit or a model at capacity goes on by itself after this long, then 4 and 10 times as long, before it fails (default 30s; 0: it fails at once)")
	runFlagSet = run.Flags().Changed

	var name, account string
	var noService, yes, passwordStdin bool
	pair := &cobra.Command{
		Use:     "login [server]",
		Aliases: []string{"pair"},
		Short:   "Log this machine in to your Aide server with your account and password",
		Long: `Log this machine in to your own Aide server with your account and
password: the server adds it to your machines by itself, with no one
approving and nothing to compare. The server is the argument (or
$AIDE_SERVER); there is no default. The account is --account, else
$AIDE_ACCOUNT, else asked for; the password is asked for (or read from
stdin with --password-stdin, or taken from $AIDE_PASSWORD). Then it
installs the background service.`,
		Example: `  aided login relay.example.com --name build-box
  echo "$PASSWORD" | aided login relay.example.com --account me --password-stdin`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var arg string
			if len(args) > 0 {
				arg = args[0]
			}
			address := connaddr.Server(arg)
			// Run again on a machine logged in whose owner is still there (the
			// install command, updating): start the service on this program.
			if c, running, err := machineCheck(cmd.Context(), dataDir, openMachine); err == nil && c.Paired &&
				!machine.OwnerGone(cmd.Context(), http.DefaultClient, c.RelayURL, c.OwnerID) {
				fmt.Printf("This machine is already logged in as %q.\n", c.Name)
				switch {
				case service.Running():
					if err := service.Restart(); err != nil {
						return err
					}
					fmt.Println("✓ The service restarted on Aide", machine.Version)
				case running:
					fmt.Println("aided runs by hand here: restart it to use Aide", machine.Version)
				case !noService:
					bin, _ := os.Executable()
					switch err := service.Install(bin, dataDir, filepath.Join(dataDir, "logs")); {
					case errors.Is(err, service.ErrNoSystemd):
						fmt.Println("No background service:", err)
					case err != nil:
						return err
					default:
						fmt.Println("✓ Background service on, Aide", machine.Version)
					}
				}
				return nil
			}
			// Not logged in yet (or its owner is gone): logging in needs the
			// person's own server, typed; there is no default to fall to.
			if address == "" {
				return connaddr.ErrNoServer
			}
			if account == "" {
				account = brandenv.Get("ACCOUNT")
			}
			password := brandenv.Get("PASSWORD")
			if passwordStdin {
				l, err := bufio.NewReader(os.Stdin).ReadString('\n')
				if err != nil && l == "" {
					return fmt.Errorf("reading the password from stdin: %w", err)
				}
				password = strings.TrimRight(l, "\r\n")
			}
			if account == "" || password == "" {
				t, err := newTerminal()
				if err != nil {
					return fmt.Errorf("%w: give --account and the password (--password-stdin or $AIDE_PASSWORD)", err)
				}
				if account == "" {
					if account, err = t.line("Account: "); err != nil {
						return err
					}
				}
				if password == "" {
					if password, err = t.secret("Password: "); err != nil {
						return err
					}
				}
			}
			n, err := openMachine()
			restart := false
			if err != nil && service.Installed() {
				// Logging in again (e.g. after the owner erased everything):
				// the running service holds the data directory.
				fmt.Println("Stopping the background service to log this machine in…")
				if uerr := service.Uninstall(); uerr == nil {
					restart = true
					n, err = openMachine()
				}
			}
			if err != nil {
				return err
			}
			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			if err := n.LogIn(ctx, address, account, password, name, os.Stdout); err != nil {
				n.Close()
				return err
			}
			infos := n.Discover(ctx)
			n.Close()
			var noServiceWhy error // said last, where it is seen
			pid, up := 0, false
			if !noService || restart {
				bin, _ := os.Executable()
				switch err := service.Install(bin, dataDir, filepath.Join(dataDir, "logs")); {
				case err != nil:
					// Online now all the same, until the computer restarts.
					noServiceWhy = err
					if pid, err = service.RunInBackground(bin, dataDir); err != nil {
						fmt.Fprintln(os.Stderr, "Could not start aided in the background either:", err)
					}
					up = err == nil
				case runtime.GOOS == "linux":
					fmt.Println("✓ Background service on: starts with the server, keeps running after you log out")
					up = true
				default:
					fmt.Println("✓ Background service on")
					up = true
				}
			}
			// The agents as the running aided finds them, in its own
			// environment, which may lack what this terminal has.
			local, fromService := infos, false
			if up {
				if c, ok := serviceCheck(cmd.Context(), dataDir); ok {
					infos, fromService = c.Agents, true
				}
			}
			fmt.Println("\nCoding agents")
			ready := false
			for _, name := range []string{"claude", "codex"} {
				if i := infos[name]; fromService && local[name].LoggedIn && i.Available && !i.LoggedIn {
					fmt.Printf("  %-10s %s, signed in in this terminal but NOT for aided → export what signs it in from ~/.profile, then run `aided service restart`\n", name, i.Version)
					continue
				}
				ready = report(name, infos[name]) || ready
			}
			for _, a := range acp.Agents {
				if i := infos[a.Name]; i.Available {
					fmt.Printf("  %-10s %s, sign-in not checked\n", a.Name, i.Version)
				}
			}
			if !ready {
				fmt.Println("  Install one as this user and sign in once (run `claude`); `aided doctor` checks again.")
			}
			fmt.Printf("\nNext\n  add a project   aided project add ~/code/app   (or in the Aide app)\n")
			fmt.Printf("  check it all    aided doctor\n  its log         %s\n  every command   aided --help\n",
				logHint(filepath.Join(dataDir, "logs", "aided.log"), true))
			if noServiceWhy != nil {
				why, fix := noServiceWhy.Error(), "fix that, then run `aided service install`"
				if errors.Is(noServiceWhy, service.ErrNoSystemd) {
					why, fix = "this system has no systemd for this user (a container, or WSL)", "make `aided run` the container's command, or start it at boot yourself"
				}
				fmt.Fprintf(os.Stderr, "\n⚠ No background service: %s\n", why)
				if !up {
					return errors.New("aided is not running: this machine is offline; " + fix + ", or run `aided run`")
				}
				fmt.Fprintf(os.Stderr, "  aided runs now (pid %d), but won't start again after a restart: %s\n", pid, fix)
			}
			return nil
		},
	}
	pair.Flags().StringVar(&name, "name", "", "machine name shown to the owner (default: hostname)")
	pair.Flags().StringVar(&account, "account", "", "the owner's account (default: $AIDE_ACCOUNT, else asked for)")
	pair.Flags().BoolVar(&passwordStdin, "password-stdin", false, "read the password from stdin")
	pair.Flags().BoolVar(&noService, "no-service", false, "do not install the background service")
	pair.Flags().BoolVar(&yes, "yes", false, "")
	pair.Flags().MarkDeprecated("yes", "it did nothing on a server of one's own")

	doctor := &cobra.Command{
		Use:   "doctor",
		Short: "Check this machine's setup, and say what to fix",
		Long:  "Check the login, the coding agents, the background service and the connection to the server. It exits 1 when something needs fixing, and says how.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			// The app's aided holds the machine: ask it.
			if text, err := askDiagnostics(cmd.Context(), paths.LocalAPI(dataDir)); err == nil {
				fmt.Print(text)
				return nil
			}
			c, running, err := machineCheck(cmd.Context(), dataDir, openMachine)
			if err != nil {
				return err
			}
			ok := true
			fmt.Printf("machine id   %s\n", c.MachineID)
			if c.Paired {
				fmt.Printf("logged in    yes, as %q\n", c.Name)
			} else {
				fmt.Println("logged in    NO  → run: aided login <server>")
				ok = false
			}
			fmt.Printf("PATH         %s\n", shellenv.Path())
			for _, name := range []string{"claude", "codex"} {
				ok = report(name, c.Agents[name]) && ok
			}
			for _, a := range acp.Agents { // optional: listed when installed
				if i := c.Agents[a.Name]; i.Available {
					fmt.Printf("  %-10s %s at %s, login not checked\n", a.Name, i.Version, i.Binary)
				}
			}
			ok = serviceLine(running) && ok
			if c.Power != nil && c.Power.Supported {
				fmt.Printf("power        %s\n", powerLine(c.Power))
			}
			switch {
			case running && c.Connected:
				fmt.Printf("relay        connected (%s)\n", c.RelayURL)
			case c.RelayURL != "":
				url := c.RelayURL
				rc := relayv1connect.NewRelayPublicServiceClient(http.DefaultClient, url)
				ctx, cancel := context.WithTimeout(cmd.Context(), 10*time.Second)
				info, err := rc.GetInfo(ctx, &relayv1.GetInfoRequest{})
				cancel()
				switch {
				case err != nil:
					fmt.Printf("relay        UNREACHABLE (%s): %v\n", url, err)
					ok = false
				case running:
					fmt.Printf("relay        reachable (%s), not connected yet → its log: %s\n", url, logHint(filepath.Join(dataDir, "logs", "aided.log"), false))
				default:
					fmt.Printf("relay        ok (%s, %s)\n", url, info.RelayId)
				}
			}
			if !ok {
				os.Exit(1)
			}
			return nil
		},
	}

	projects := projectCommand(&dataDir, openMachine)
	status := &cobra.Command{
		Use:   "status",
		Short: "Show this machine: its name, whether it is connected, the service and its projects",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, running, err := machineCheck(cmd.Context(), dataDir, openMachine)
			if err != nil {
				return err
			}
			paired := "not logged in → run: aided login <server>"
			if c.Paired {
				paired = fmt.Sprintf("logged in as %q, server %s", c.Name, c.RelayURL)
				if running && c.Connected {
					paired += ", connected"
				}
			}
			fmt.Printf("machine   %s · %s · Aide %s\n", c.MachineID, paired, c.Version)
			serviceLine(running)
			fmt.Printf("updates      %s\n", update.Line(dataDir))
			if c.Power != nil && c.Power.Supported {
				fmt.Printf("power        %s\n", powerLine(c.Power))
			}
			fmt.Println()
			list, _, _ := projects.Find([]string{"list"})
			return list.RunE(cmd, nil)
		},
	}

	audit := &cobra.Command{
		Use:   "audit-verify",
		Short: "Verify the tamper-evident audit log",
		RunE: func(cmd *cobra.Command, _ []string) error {
			n, err := openMachine()
			if err != nil {
				return err
			}
			defer n.Close()
			count, err := n.VerifyAudit()
			if err != nil {
				return fmt.Errorf("audit log INVALID after %d records: %w", count, err)
			}
			fmt.Printf("audit log ok: %d records\n", count)
			return nil
		},
	}

	svc := &cobra.Command{Use: "service", Short: "The background service: status, install, restart, uninstall",
		Long: `aided runs in the background as a service of this user: a systemd user
service on Linux (with lingering, so it starts at boot with nobody logged
in), a LaunchAgent on macOS, a Task Scheduler logon task on Windows.
"aided login" installs it.`}
	var serviceOwner bool
	install := &cobra.Command{Use: "install", Short: "Install and start the service (again)", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		bin, _ := os.Executable()
		var args []string
		if serviceOwner {
			args = []string{"--owner"}
		}
		if err := service.Install(bin, dataDir, filepath.Join(dataDir, "logs"), args...); err != nil {
			return err
		}
		serviceLine(false)
		return nil
	}}
	// The Windows app's daemon: the owner's, and this PC's machine (docs/windows.md §6.1).
	install.Flags().BoolVar(&serviceOwner, "owner", false, "also run the owner role")
	install.Flags().MarkHidden("owner")
	svc.AddCommand(&cobra.Command{Use: "status", Short: "Whether the service runs, starts at boot, and where its log is", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if !serviceLine(false) {
				os.Exit(1)
			}
			return nil
		}},
		install,
		&cobra.Command{Use: "restart", Short: "Restart the service (after an update)", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
			if err := service.Restart(); err != nil {
				return err
			}
			fmt.Println("✓ Restarted")
			return nil
		}},
		&cobra.Command{Use: "uninstall", Short: "Stop the service and stop starting it; the login and projects stay", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
			if err := service.Uninstall(); err != nil {
				return err
			}
			fmt.Println("✓ The service is stopped and won't start again; `aided service install` brings it back")
			return nil
		}})
	logFile := filepath.Join(dataDir, "logs", "aided.log")
	startsWhen := "at boot"
	if runtime.GOOS != "linux" { // a LaunchAgent, a logon task
		startsWhen = "at login"
	}
	serviceLine = func(byHand bool) bool {
		switch {
		case service.Running() && service.AtBoot():
			fmt.Printf("service      running, starts %s · log: %s\n", startsWhen, logHint(logFile, true))
			return true
		case service.Running():
			fmt.Printf("service      running, but NOT %s → run: aided service install · log: %s\n", startsWhen, logHint(logFile, true))
			return false
		case service.Installed():
			fmt.Printf("service      installed, NOT RUNNING → run: aided service restart · log: %s\n", logHint(logFile, false))
			return false
		case byHand:
			fmt.Printf("service      not installed; aided runs by hand → to start it %s: aided service install\n", startsWhen)
			return false
		default:
			fmt.Println("service      not installed → run: aided service install")
			return false
		}
	}

	var updateCheck, updateYes bool
	upd := &cobra.Command{
		Use:   "update",
		Short: "Update Aide here to the latest release and restart the service",
		Long: `Update aided and aide, where they are installed, to the latest signed
release, and restart the background service. When that would stop a
session, it asks first.

Aide also updates itself when no session is running, unless
"aided update auto off" turned that off.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return update.Run(cmd.Context(), cmd.InOrStdin(), cmd.OutOrStdout(), dataDir, updateCheck, updateYes)
		},
	}
	upd.Flags().BoolVar(&updateCheck, "check", false, "only say whether a newer version exists")
	upd.Flags().BoolVar(&updateYes, "yes", false, "update without asking, even when that stops a session")
	upd.AddCommand(&cobra.Command{Use: "auto [on|off]", Short: "Turn automatic updates on or off, or say which", Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return update.Auto(cmd.OutOrStdout(), dataDir, strings.Join(args, ""))
		}})

	helper := &cobra.Command{Use: "spawn-helper", Hidden: true, Args: cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error { return spawn.Serve(os.NewFile(3, "spawn-helper")) }}
	// The tools a session's coding agent starts (docs/agent-messages.md §7.4).
	messageMCP := &cobra.Command{Use: "message-mcp", Hidden: true, Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error { return machine.ServeMessageMCP(cmd.Context()) }}
	// The Windows app's installer puts its programs' folder on the user's PATH, and takes it off (docs/windows.md §6.1).
	userPath := &cobra.Command{Use: "user-path add|remove <dir>", Hidden: true, Args: cobra.ExactArgs(2),
		RunE: func(_ *cobra.Command, args []string) error {
			if args[0] != "add" && args[0] != "remove" {
				return fmt.Errorf("user-path: add or remove, not %q", args[0])
			}
			return shellenv.OnUserPath(args[1], args[0] == "add")
		}}
	root.AddCommand(eraseCommand(&dataDir), uninstallCommand(&dataDir), dshBridgeCommand())
	root.AddCommand(run, pair, doctor, status, audit, svc, upd, helper, messageMCP, userPath, projects, rootsCommand(&dataDir, openMachine), configCommand(&dataDir, openMachine),
		&cobra.Command{Use: "version", Short: "Print the version", Args: cobra.NoArgs, Run: func(*cobra.Command, []string) { fmt.Println(machine.Version) }})
	return root
}

// powerLine says whether the running aided saves power, and why (docs/resident.md §2.3).
func powerLine(p *resident.State) string {
	line := "normal (" + p.Reason + ")"
	if p.Saving {
		line = "saving: throttled, " + p.Priority + " priority (" + p.Reason + ")"
	}
	if !p.Since.IsZero() {
		line += " since " + p.Since.Local().Format("15:04:05")
	}
	if p.Error != "" {
		line += " · error: " + p.Error
	}
	return line
}

// serviceLine prints the background service's state (byHand: an aided
// runs here outside it), and whether it is fine; set in rootCommand, where
// the log's place is known.
var serviceLine func(byHand bool) bool

// machineCheck asks the running machine how it is (it holds the data
// directory), or opens it when it isn't running.
// machineDirs are where this user's machines keep their data: aided
// login's own first, then the app's machine in the current owner profile,
// then in the other profiles.
func machineDirs(dataDir string) []string {
	dirs := []string{filepath.Join(dataDir, "machine")}
	owner := filepath.Join(dataDir, "owner")
	if cur, err := os.ReadFile(filepath.Join(owner, "current")); err == nil {
		if id := strings.TrimSpace(string(cur)); id != "" && !strings.ContainsAny(id, `/\`) {
			dirs = append(dirs, filepath.Join(owner, "profiles", id, "machine"))
		}
	}
	others, _ := filepath.Glob(filepath.Join(owner, "profiles", "*", "machine"))
	for _, d := range others {
		if !slices.Contains(dirs, d) {
			dirs = append(dirs, d)
		}
	}
	return dirs
}

func machineCheck(ctx context.Context, dataDir string, open func() (*machine.Machine, error)) (machine.Check, bool, error) {
	var c machine.Check
	// The machine that runs answers: aided login's own, or the app's in its
	// owner profile. Asking only the first, status read the app's PC as
	// never logged in (the machine from before its profile) while doctor,
	// which asks the app's aided, said it was.
	for _, dir := range machineDirs(dataDir) {
		if out, ok, err := machine.ControlRequest(ctx, dir, "GET", "/check", nil); ok {
			if err == nil {
				err = json.Unmarshal(out, &c)
			}
			return c, true, err
		}
	}
	n, err := open()
	if err != nil {
		return c, false, err
	}
	defer n.Close()
	return n.Check(ctx), false, nil
}

// serviceCheck asks the aided that just started for its check, once it
// listens.
func serviceCheck(ctx context.Context, dataDir string) (machine.Check, bool) {
	var c machine.Check
	for deadline := time.Now().Add(15 * time.Second); time.Now().Before(deadline); time.Sleep(300 * time.Millisecond) {
		if out, ok, err := machine.ControlRequest(ctx, filepath.Join(dataDir, "machine"), "GET", "/check", nil); ok {
			return c, err == nil && json.Unmarshal(out, &c) == nil
		}
	}
	return c, false
}

// askDiagnostics asks the aided serving sock for its diagnostics.
func askDiagnostics(ctx context.Context, sock string) (string, error) {
	if _, err := os.Stat(sock); err != nil {
		return "", err
	}
	hc := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return localipc.Dial(ctx, sock)
	}}}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	res, err := localv1connect.NewLocalServiceClient(hc, "http://local").GetDiagnostics(ctx, &localv1.GetDiagnosticsRequest{})
	if err != nil {
		return "", err
	}
	return res.Text, nil
}

// providers returns the coding agents; AIDE_FAKE_PROVIDERS=1 replaces
// them with a scripted fake (tests only; it can only be set locally).
func providers() map[string]provider.Provider {
	if os.Getenv("AIDE_FAKE_PROVIDERS") == "1" {
		return map[string]provider.Provider{"claude": fake.Provider{}, "codex": fake.Provider{}}
	}
	ps := machine.DefaultProviders()
	if b := brandenv.Get("CODEX_BIN"); b != "" {
		ps["codex"] = &codex.Provider{PathEnv: shellenv.Path(), Binary: b}
	}
	if b := brandenv.Get("CLAUDE_BIN"); b != "" {
		ps["claude"] = &claude.Provider{PathEnv: shellenv.Path(), Binary: b}
	}
	for _, a := range acp.Agents {
		if b := brandenv.Get(strings.ToUpper(a.Name) + "_BIN"); b != "" {
			ps[a.Name] = &acp.Provider{Agent: a, PathEnv: shellenv.Path(), Binary: b}
		}
	}
	return ps
}

func isDir(path string) bool {
	st, err := os.Stat(path)
	return err == nil && st.IsDir()
}

// installPages are where the coding agents Aide checks are installed from.
var installPages = map[string]string{
	"claude":      "https://github.com/anthropics/claude-code",
	"codex":       "https://github.com/openai/codex",
	"antigravity": "https://github.com/agentclientprotocol/registry/blob/main/antigravity-acp/agent.json",
}

func report(name string, i provider.Info) bool {
	switch {
	case !i.Available:
		fmt.Printf("  %-10s not installed: %s\n", name, installPages[name])
		return false
	case !i.LoggedIn:
		command := name
		if name == "antigravity" {
			command = "aide agent login antigravity"
		}
		fmt.Printf("  %-10s %s, NOT SIGNED IN → run `%s` once as this user (on a server it prints a link to open on any device)\n", name, i.Version, command)
		return false
	default:
		fmt.Printf("  %-10s %s, signed in\n", name, i.Version)
		return true
	}
}

// eraseMachine erases a machine whose owner erased everything
// (docs/erase-everything.md), and takes its service away, so that it waits
// to be paired as a newly installed one does. The rest of dataDir stays: on
// a Mac it is also the app's (its owners' profiles).
func eraseMachine(dataDir string, cfg machine.Config) error {
	if err := machine.EraseHer(cfg); err != nil {
		fmt.Fprintf(os.Stderr, "warning: Her's data stays: %v\n", err)
	}
	kept, err := eraseHere(dataDir, cfg)
	if err != nil {
		return err
	}
	fmt.Fprintln(os.Stderr, "The owner erased everything: Aide's data on this machine is erased. Log it in again with `aided login <server>`.")
	for _, wt := range kept {
		fmt.Fprintf(os.Stderr, "Kept, with uncommitted changes: %s\n", wt)
	}
	if service.InstalledFor(dataDir) {
		return service.Uninstall()
	}
	return nil
}
