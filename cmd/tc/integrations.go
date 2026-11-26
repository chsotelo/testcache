package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/chsotelo/testcache/internal/integrate"
	"github.com/chsotelo/testcache/internal/runlog"
)

const (
	// shellShimName is the name tc is installed under to act as the npm/pnpm script shell.
	shellShimName = "tc-shell"
	// exitCodeShellMissing is what a shell reports when it cannot run a command.
	exitCodeShellMissing = 127
)

// isShellShim reports whether tc was started through its tc-shell link.
func isShellShim(argv0 string) bool {
	return filepath.Base(argv0) == shellShimName
}

// runScriptShell is the npm/pnpm script shell. A package script that is exactly one Vitest
// single run is served by `tc vitest`; everything else replaces this process with /bin/sh
// and the untouched arguments, so stdio, signals and the exit code are the real shell's.
func runScriptShell(shellArgs []string, workDir string, stdout, stderr io.Writer) int {
	plan := planScriptShell(shellArgs, workDir)
	if !plan.Rewrite {
		return execRealShell(shellArgs, stderr)
	}
	for _, assignment := range plan.Env {
		name, value, _ := strings.Cut(assignment, "=")
		_ = os.Setenv(name, value)
	}
	_ = os.Setenv(runlog.EnvInvokedBy, string(runlog.InvokerScriptShell))
	return runVitest(plan.VitestArgs, workDir, stdout, stderr)
}

// planScriptShell never lets a tc bug reach a package script: on a panic nothing is rewritten.
func planScriptShell(shellArgs []string, workDir string) (plan integrate.ScriptShellPlan) {
	defer func() {
		if recover() != nil {
			plan = integrate.ScriptShellPlan{}
		}
	}()
	plan = integrate.PlanScriptShell(shellArgs)
	if plan.Rewrite && !integrate.ResolvesProjectVitest(workDir) {
		return integrate.ScriptShellPlan{}
	}
	return plan
}

func execRealShell(shellArgs []string, stderr io.Writer) int {
	argv := append([]string{filepath.Base(integrate.RealShell)}, shellArgs...)
	// execve(2): on success this process becomes the shell and the lines below never run.
	replaceProcess := syscall.Exec
	err := replaceProcess(integrate.RealShell, argv, os.Environ())
	fmt.Fprintf(stderr, "tc: exec %s: %v\n", integrate.RealShell, err)
	return exitCodeShellMissing
}

// runClaudeHook is the Claude Code PreToolUse hook. It always exits 0: a hook that fails
// must not get in the way of the tool call.
func runClaudeHook(stdin io.Reader, stdout, stderr io.Writer) int {
	defer func() {
		if r := recover(); r != nil {
			fmt.Fprintf(stderr, "tc claude-hook: internal error: %v\n", r)
		}
	}()
	if err := integrate.NewHookRewriter().RunClaudeHook(stdin, stdout); err != nil {
		fmt.Fprintf(stderr, "tc claude-hook: %v\n", err)
	}
	return 0
}
