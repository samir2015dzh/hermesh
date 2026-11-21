package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/spf13/cobra"

	localv1 "github.com/samir2015dzh/aide/gen/go/aide/local/v1"
	"github.com/samir2015dzh/aide/gen/go/aide/local/v1/localv1connect"
	"github.com/samir2015dzh/aide/internal/brandenv"
	"github.com/samir2015dzh/aide/internal/localipc"
	"github.com/samir2015dzh/aide/internal/machine"
	"github.com/samir2015dzh/aide/internal/paths"
	"github.com/samir2015dzh/aide/internal/service"
)

// uninstallCommand is `aided uninstall` (docs/aided.md): it removes
// what the install script put here for this user, after one question (and
// one more where Her's data is kept).
func uninstallCommand(dataDir *string) *cobra.Command {
	var yes, eraseHer bool
	cmd := &cobra.Command{
		Use:   "uninstall",
		Short: "Remove Aide for this user on this computer: its machine, logged out, the background service, its data and its programs",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if runtime.GOOS == "windows" {
				return errors.New("on Windows, uninstall Aide in Settings › Apps; after an install.ps1 install, run `aided erase`, then delete its programs' folder and data folder")
			}
			bin, err := os.Executable()
			if err != nil {
				return err
			}
			if b, err := filepath.EvalSymlinks(bin); err == nil {
				bin = b
			}
			if strings.Contains(bin, ".app/Contents/") {
				return errors.New("this aided is the Aide app's: erase Aide in the app (Settings › Account & Devices), then move the app to the Trash")
			}
			ask := func(prompt string) (string, error) {
				t, err := newTerminal()
				if err != nil {
					return "", fmt.Errorf("%w (or pass --yes)", err)
				}
				return t.line(prompt)
			}
			if yes {
				ask = nil
			}
			return uninstall(cmd.Context(), os.Stdout, ask, *dataDir, bin, eraseHer)
		},
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "don't ask")
	cmd.Flags().BoolVar(&eraseHer, "erase-her", false, "delete Her's data too, without asking")
	return cmd
}

// uninstall removes Aide for this user: the machine in dataDir,
// unpaired, the service, dataDir (and where it links to), the CLI's home
// and the programs, aided at bin and aide, her and her-console/
// beside it. Her's data, where Her was on, goes with eraseHer, or when ask
// is answered so; else it stays. ask, when not nil, asks first.
func uninstall(ctx context.Context, w io.Writer, ask func(string) (string, error), dataDir, bin string, eraseHer bool) error {
	home, _ := os.UserHomeDir()
	realHome := home
	if r, err := filepath.EvalSymlinks(home); err == nil {
		realHome = r
	}
	realData := dataDir
	if r, err := filepath.EvalSymlinks(dataDir); err == nil {
		realData = r
	}
	if inside(realHome, realData) || inside(bin, realData) {
		return fmt.Errorf("the data directory %s holds more than Aide's data; not removing it", dataDir)
	}
	keptBefore := filepath.Join(dataDir, "kept-worktrees")
	if e, _ := os.ReadDir(keptBefore); len(e) > 0 {
		return fmt.Errorf("%s holds worktrees with uncommitted changes, kept when Aide was erased before: move what you want out of it (git worktree move), then uninstall", keptBefore)
	}
	cfg := machine.Config{DataDir: filepath.Join(dataDir, "machine")}
	isMachine := exists(machine.KeyPath(cfg.DataDir))
	if !isMachine && exists(filepath.Join(dataDir, "owner")) {
		if err := checkLeftAsDevice(ctx, dataDir); err != nil {
			return err
		}
	}
	svc := service.InstalledFor(dataDir)
	machineLine := "its machine: logged out, and gone from your machines"
	herHome, herData := filepath.Join(home, ".her"), false
	if isMachine {
		c, running, err := machineCheck(ctx, dataDir, func() (*machine.Machine, error) { return machine.Open(cfg) })
		if (running || err != nil) && !svc {
			return errors.New("aided runs here outside its service: stop `aided run`, then uninstall")
		}
		if c.Paired {
			machineLine = fmt.Sprintf("its machine “%s” (owner %s): logged out, and gone from your machines", c.Name, c.OwnerID)
		}
		if c.Busy > 0 {
			machineLine += fmt.Sprintf("; the %d sessions running now stop", c.Busy)
		}
		if h, ok, err := machine.HerData(cfg); err == nil {
			herHome, herData = h, ok
		}
	}
	eraseHer = eraseHer && herData
	askHer := herData && !eraseHer && ask != nil
	cliHome := paths.CLIHome()
	programs := []string{bin}
	if p := filepath.Join(filepath.Dir(bin), "aide"); exists(p) {
		programs = append([]string{p}, programs...)
	}
	for _, name := range []string{"her", "her-console", "her-computer", "her-pdf", "aide-screen"} {
		if p := filepath.Join(filepath.Dir(bin), name); exists(p) {
			programs = append(programs, p)
		}
	}
	keepDir := filepath.Join(home, "aide-kept-worktrees")

	name := os.Getenv("USER")
	if u, err := user.Current(); err == nil {
		name = u.Username
	}
	fmt.Fprintf(w, "This removes Aide for %s on this computer:\n", name)
	if isMachine {
		fmt.Fprintf(w, "  • %s\n", machineLine)
	}
	if svc {
		fmt.Fprintln(w, "  • the background service")
	}
	if exists(dataDir) {
		where := dataDir
		if realData != dataDir {
			where += " → " + realData
		}
		fmt.Fprintf(w, "  • its data: %s (sessions, logs, the worktrees Aide made)\n", where)
	} else if os.Getuid() == 0 {
		fmt.Fprintln(w, "  (root has no Aide data here: installed for another user? Run `su - <user> -c 'aided uninstall'`)")
	}
	if exists(cliHome) {
		fmt.Fprintf(w, "  • the agents' CLI key and settings: %s\n", cliHome)
		if exists(filepath.Join(cliHome, "state.json")) {
			fmt.Fprintln(w, "    (an agent signed in with it stays listed on your devices until you revoke it there)")
		}
	}
	if eraseHer {
		fmt.Fprintf(w, "  • Her's assistants, conversations and settings: %s\n", herHome)
	}
	fmt.Fprintf(w, "  • the programs: %s\n", strings.Join(programs, ", "))
	fmt.Fprintln(w, "Stays: your project folders and repositories, branches included.")
	if exists(herHome) && !eraseHer && !askHer {
		fmt.Fprintf(w, herStays, herHome)
	}
	if isMachine {
		fmt.Fprintf(w, "Worktrees with uncommitted changes move to %s.\n", keepDir)
	}
	if ask != nil {
		if a, err := ask("This can't be undone. Uninstall? [y/N] "); err != nil || !strings.EqualFold(a, "y") {
			if err != nil {
				return err
			}
			return errors.New("not uninstalled")
		}
	}
	if askHer {
		a, err := ask(fmt.Sprintf(herAsk, herHome))
		if err != nil {
			return err
		}
		if eraseHer = strings.EqualFold(a, "y"); !eraseHer {
			fmt.Fprintf(w, herStays, herHome)
		}
	}

	// The service would start it again at once.
	if svc {
		if err := service.Uninstall(); err != nil {
			return fmt.Errorf("stopping the background service: %w", err)
		}
	}
	var kept []string
	if isMachine {
		if n, err := openWhenStopped(cfg); err == nil {
			if n.Paired() {
				lctx, cancel := context.WithTimeout(ctx, 15*time.Second)
				if err := n.LeaveOwner(lctx); err != nil {
					fmt.Fprintf(os.Stderr, "warning: the server didn't remove it from its owner (%v); remove it in the Aide app\n", err)
				}
				cancel()
			}
			n.Close()
		}
		if eraseHer {
			eraseHerData(cfg, herHome)
		}
		var err error
		if kept, err = machine.Erase(cfg, keepDir); err != nil {
			return fmt.Errorf("erasing this machine: %w", err)
		}
	}
	keepData := false
	for _, wt := range kept {
		if !inside(keepDir, wt) {
			keepData = true // it couldn't be moved out
		}
	}
	if !keepData {
		if err := os.RemoveAll(realData); err != nil {
			return err
		}
		os.Remove(dataDir) // the link to it, if it was one
	}
	if err := os.RemoveAll(cliHome); err != nil {
		return err
	}
	// A her left from before Aide dropped her: the browsers here would go on
	// starting it for the Aide extension once it is gone. Never one
	// $AIDE_HER_BIN names (tests and development), which has a home of its own.
	if her := filepath.Join(filepath.Dir(bin), "her"); brandenv.Get("HER_BIN") == "" && exists(her) {
		exec.CommandContext(ctx, her, "browser", "extension", "uninstall").Run()
	}
	for _, p := range programs {
		if err := os.RemoveAll(p); err != nil {
			return err
		}
	}

	fmt.Fprintf(w, "✓ Aide is uninstalled for %s.\n", name)
	for _, wt := range kept {
		fmt.Fprintf(w, "  Kept, with uncommitted changes: %s\n", wt)
	}
	if keepData {
		fmt.Fprintf(w, "  %s stays, for the worktrees above: delete it once they are safe\n", realData)
	}
	if lingering(name) {
		fmt.Fprintf(w, "Lingering stays on for %s, as other services of %s's may need it; to turn it off: sudo loginctl disable-linger %s\n", name, name, name)
	}
	return nil
}

// What `aided uninstall` and `erase` say of Her's data, where Her was
// on (docs/her-server.md §5.6).
const (
	herAsk   = "Also delete Her's data in %s? [y/N] "
	herStays = "Stays too: Her's assistants, conversations and settings, in %s; delete it yourself to remove them.\n"
)

// eraseHerData deletes Her's data, for the machine in cfg, stopped, before
// it is erased: it tells where Her was on.
func eraseHerData(cfg machine.Config, home string) {
	if err := machine.EraseHer(cfg); err != nil {
		fmt.Fprintf(os.Stderr, "warning: Her's data stays in %s: %v\n", home, err)
	}
}

// checkLeftAsDevice refuses while this computer is one of the owner's
// devices (logged in through its owner role): sign it out first, from
// another device, so that it is not left listed.
func checkLeftAsDevice(ctx context.Context, dataDir string) error {
	sock := paths.LocalAPI(dataDir)
	hc := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return localipc.Dial(ctx, sock)
	}}}
	st, err := localv1connect.NewLocalServiceClient(hc, "http://local").GetStatus(ctx, &localv1.GetStatusRequest{})
	switch {
	case err != nil:
		return fmt.Errorf("this computer is set up as one of your devices, and whether it still is one can't be checked (start it with `aided run --owner --no-machine`): %w", err)
	case st.Initialized && !st.Removed:
		return errors.New("this computer is one of your devices: sign it out from another of your devices (Settings › Account), then uninstall")
	}
	return nil
}

// inside reports whether path is dir or under it.
func inside(path, dir string) bool {
	rel, err := filepath.Rel(dir, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

// lingering reports whether systemd keeps the user's services running
// with nobody logged in; the installer turned it on.
func lingering(name string) bool {
	if runtime.GOOS != "linux" {
		return false
	}
	out, err := exec.Command("loginctl", "show-user", name, "--property=Linger").Output()
	return err == nil && strings.TrimSpace(string(out)) == "Linger=yes"
}
