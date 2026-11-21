//go:build unix

package paths

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

func dataDir() string {
	home, _ := os.UserHomeDir()
	if runtime.GOOS == "darwin" {
		return filepath.Join(home, "Library", "Application Support", "Aide")
	}
	if x := os.Getenv("XDG_DATA_HOME"); x != "" {
		return filepath.Join(x, "aide")
	}
	return filepath.Join(home, ".local", "share", "aide")
}

func cliHome() string {
	if x := os.Getenv("XDG_CONFIG_HOME"); x != "" {
		return filepath.Join(x, "aide")
	}
	h, _ := os.UserHomeDir()
	return filepath.Join(h, ".config", "aide")
}

// Endpoints are Unix sockets, whose paths have a short limit: a long data
// directory's go to the temp folder.

// LocalAPI is where the owner role serves LocalService.
func LocalAPI(dataDir string) string {
	p := filepath.Join(dataDir, "run", "aided.sock")
	if len(p) > 100 {
		p = filepath.Join(os.TempDir(), fmt.Sprintf("aide-%d", os.Getuid()), "aided.sock")
	}
	return p
}

// Control is where a machine with this data directory takes its own
// user's commands; a long path's is named after the directory, so two
// machines of one user never share it.
func Control(dataDir string) string {
	p := filepath.Join(dataDir, "control.sock")
	if len(p) > 100 {
		abs, _ := filepath.Abs(dataDir)
		h := sha256.Sum256([]byte(abs))
		p = filepath.Join(os.TempDir(), fmt.Sprintf("aide-control-%d-%x.sock", os.Getuid(), h[:8]))
	}
	return p
}

// Tools is where the sessions of the machine machineID reach their tools.
func Tools(dataDir, machineID string) string {
	p := filepath.Join(dataDir, "tools.sock")
	if len(p) > 100 {
		p = filepath.Join(os.TempDir(), "aide-"+strings.TrimPrefix(machineID, "m_")[:12]+".sock")
	}
	return p
}

// Link is where the CLI with this home keeps its relay connection between
// commands (the CLI's link, docs/cli.md); a long path's is named after the
// home, so two agents of one user never share it.
func Link(cliHome string) string {
	p := filepath.Join(cliHome, "link", "link.sock")
	if len(p) > 100 {
		abs, _ := filepath.Abs(cliHome)
		h := sha256.Sum256([]byte(abs))
		p = filepath.Join(os.TempDir(), fmt.Sprintf("aide-link-%d-%x.sock", os.Getuid(), h[:8]))
	}
	return p
}

// LinkLock is the folder whose lock says the link of this home runs.
func LinkLock(cliHome string) string { return filepath.Join(cliHome, "link") }

// localDir is this user's own folder in the temp directory (the same for
// what launchd or systemd starts and for a shell), as LocalAPI's is.
func localDir() string { return filepath.Join(os.TempDir(), fmt.Sprintf("aide-%d", os.Getuid())) }

const localExt = ".sock"
