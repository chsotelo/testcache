package main

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/chsotelo/testcache/internal/integrate"
)

// version is stamped at build time via -ldflags "-X main.version=...".
var version = "dev"

type cliSubcommand string

const (
	programName                  = "tc"
	cmdVitest      cliSubcommand = "vitest"
	cmdVerify      cliSubcommand = "verify"
	cmdLog         cliSubcommand = "log"
	cmdScriptShell cliSubcommand = "script-shell"
	cmdClaudeHook  cliSubcommand = "claude-hook"
	cmdDoctor      cliSubcommand = "doctor"
	// cmdNodePreload prints the Node preload this binary carries; scripts/install.sh installs it.
	cmdNodePreload cliSubcommand = "node-preload"
	// cmdAlertExec is internal: the detached helper that runs alertCommand.
	cmdAlertExec    cliSubcommand = "alert-exec"
	exitCodeFailure               = 1
	exitCodeUsage                 = 2
)

func main() {
	os.Exit(runProcess(os.Args))
}

// runProcess is main without the exit. A panic that nothing closer recovered ends here: as a
// script shell tc still hands the command to the real shell, otherwise it reports and fails.
func runProcess(argv []string) (code int) {
	asShell := len(argv) > 0 && isShellShim(argv[0])
	wd, err := os.Getwd()
	defer func() {
		if r := recover(); r != nil {
			fmt.Fprintf(os.Stderr, "tc: internal error: %v\n", r)
			switch {
			case asShell:
				code = execRealShell(argv[1:], os.Stderr)
			case len(argv) > 1 && argv[1] == string(cmdVitest) && err == nil:
				code = runLastResortVitest(argv[2:], wd)
			default:
				code = exitCodeFailure
			}
		}
	}()
	if asShell {
		if err != nil {
			return execRealShell(argv[1:], os.Stderr)
		}
		return runScriptShell(argv[1:], wd, os.Stdout, os.Stderr)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "tc: resolve working directory: %v\n", err)
		return exitCodeFailure
	}
	return runCLI(argv[1:], wd, os.Stdout, os.Stderr)
}

func runCLI(args []string, workDir string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		printUsage(stderr)
		return 2
	}

	switch args[0] {
	case string(cmdVitest):
		return runVitest(args[1:], workDir, stdout, stderr)
	case "explain":
		return runExplain(args[1:], workDir, stdout, stderr)
	case string(cmdVerify):
		return runVerify(args[1:], workDir, stdout, stderr)
	case string(cmdLog):
		return runLog(args[1:], workDir, stdout, stderr)
	case string(cmdScriptShell):
		return runScriptShell(args[1:], workDir, stdout, stderr)
	case string(cmdClaudeHook):
		return runClaudeHook(os.Stdin, stdout, stderr)
	case string(cmdDoctor):
		return runDoctor(stdout, stderr)
	case string(cmdNodePreload):
		_, _ = stdout.Write(integrate.NodePreloadSource())
		return 0
	case string(cmdAlertExec):
		return runAlertExec(args[1:])
	case "quarantine":
		return runQuarantine(args[1:], workDir, stdout, stderr)
	case "gc":
		return runGC(args[1:], workDir, stdout, stderr)
	case "clean":
		return runClean(args[1:], workDir, stdout, stderr)
	case "stats":
		return runStats(args[1:], workDir, stdout, stderr)
	case "version", "--version", "-v":
		fmt.Fprintf(stdout, "tc %s\n", version)
		return 0
	case "help", string(flagHelp), string(flagHelpShort):
		printUsage(stdout)
		return 0
	default:
		fmt.Fprintf(stderr, "tc: unknown command %q (run 'tc help' for usage)\n", args[0])
		return 2
	}
}

func printUsage(w io.Writer) {
	lines := []string{
		"tc - content-addressed, coverage-aware test result cache",
		"",
		"Usage:",
		"  tc vitest [--no-cache] [--no-revalidate] [vitest args] [files...]",
		"  tc explain [vitest args] [files...]",
		"  tc verify [vitest args] [files...]",
		"  tc quarantine [--clear <file>]",
		"  tc log [-n <runs>] [--anomalies] [--repo <path>] [--json]",
		"  tc script-shell -c <command>   (npm/pnpm script shell, installed as tc-shell)",
		"  tc claude-hook                 (Claude Code PreToolUse hook, reads JSON on stdin)",
		"  tc doctor                      (check the install, the integrations and logged failures)",
		"  tc gc",
		"  tc stats",
		"  tc clean [--all]",
		"  tc version",
		"  tc help",
	}
	fmt.Fprintln(w, strings.Join(lines, "\n"))
}
