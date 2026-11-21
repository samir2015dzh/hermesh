// Command aide is the client CLI: a client (usually an AI agent) pairs
// with an owner, then runs coding-agent sessions (Claude Code, Codex, ACP
// agents) on the owner's machines.
// (CI sample for work order #33, not to be merged.)
package main

import (
	"os"

	"github.com/samir2015dzh/aide/internal/cli"
)

func main() {
	os.Exit(cli.Main(os.Args[1:], os.Stdout, os.Stderr))
}
