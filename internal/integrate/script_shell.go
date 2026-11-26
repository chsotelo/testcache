package integrate

import (
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/chsotelo/testcache/internal/config"
	"github.com/chsotelo/testcache/internal/runner/vitest"
)

const (
	// RealShell is the shell every command that tc does not take over is handed to.
	RealShell = "/bin/sh"
	// shellCommandFlag is how npm and pnpm pass a package script to the script shell.
	shellCommandFlag = "-c"
)

// ScriptShellPlan says what the script shell does with one `-c <command>` call.
type ScriptShellPlan struct {
	// Rewrite is true when the command is exactly one Vitest single-run invocation.
	Rewrite bool
	// Env holds the NAME=value assignments that preceded the command.
	Env []string
	// VitestArgs are the arguments for `tc vitest`.
	VitestArgs []string
}

// PlanScriptShell decides whether the arguments npm or pnpm gave to the script shell are a
// Vitest single run that tc can serve. Anything it is not sure about is not rewritten:
// compound commands, redirections, expansions, globs, watch and UI modes, TC_DISABLE.
func PlanScriptShell(shellArgs []string) ScriptShellPlan {
	if len(shellArgs) != 2 || shellArgs[0] != shellCommandFlag || config.IsDisabled() {
		return ScriptShellPlan{}
	}
	command := shellArgs[1]
	if !strings.Contains(command, string(wordVitest)) {
		return ScriptShellPlan{}
	}
	scan, ok := splitShellCommand(command)
	if !ok || scan.operators != 0 || len(scan.segments) != 1 {
		return ScriptShellPlan{}
	}
	words := scan.segments[0].words
	for _, w := range words {
		if !w.plain {
			return ScriptShellPlan{}
		}
	}
	call, ok := findVitestCall(words)
	if !ok || call.viaRtk || !call.isSingleRun() {
		return ScriptShellPlan{}
	}
	args := make([]string, 0, len(call.args))
	for _, a := range call.args {
		args = append(args, a.text)
	}
	return ScriptShellPlan{Rewrite: true, Env: call.assignments, VitestArgs: args}
}

// ResolvesProjectVitest reports whether tc would run the very Vitest that the shell finds on
// PATH from workDir. npm and pnpm put node_modules/.bin on PATH for package scripts, so this
// holds in any project with Vitest installed.
func ResolvesProjectVitest(workDir string) bool {
	onPath, err := exec.LookPath(string(wordVitest))
	if err != nil {
		return false
	}
	resolved, err := filepath.EvalSymlinks(onPath)
	if err != nil {
		return false
	}
	bin := vitest.LocateBinary(workDir)
	return bin != "" && bin == resolved
}
