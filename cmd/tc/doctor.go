package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/chsotelo/testcache/internal/config"
	"github.com/chsotelo/testcache/internal/integrate"
	"github.com/chsotelo/testcache/internal/runlog"
)

type doctorLevel string

const (
	doctorOK   doctorLevel = "ok"
	doctorWarn doctorLevel = "warn"
	doctorFail doctorLevel = "FAIL"
	// doctorRecentRuns is how far back `tc doctor` looks in the run log.
	doctorRecentRuns = 50
	// doctorShownErrors is how many preload failures are printed in full.
	doctorShownErrors    = 3
	envScriptShellPnpmRT = "pnpm_config_script_shell"
	envScriptShellNpmRT  = "npm_config_script_shell"
	rerunInstaller       = "run scripts/install.sh again"
	notActiveInShell     = "not active in this shell (open a new one after installing)"
)

// doctorCheck is one line of `tc doctor`.
type doctorCheck struct {
	level  doctorLevel
	name   string
	detail string
}

// doctorScene is what the checks look at; tests point it at temporary directories.
type doctorScene struct {
	tcBinary string
	cacheDir string
	getenv   func(string) string
}

// runDoctor reports whether tc and its integrations are in place and whether anything on
// the way to tc has failed. It exits 1 when a check fails.
func runDoctor(stdout, stderr io.Writer) int {
	exe, err := os.Executable()
	if err != nil {
		fmt.Fprintf(stderr, "tc doctor: locate the tc binary: %v\n", err)
		return exitCodeFailure
	}
	if real, err := filepath.EvalSymlinks(exe); err == nil {
		exe = real
	}
	cacheDir, err := config.CacheBaseDir()
	if err != nil {
		fmt.Fprintf(stderr, "tc doctor: %v\n", err)
		return exitCodeFailure
	}
	checks := collectDoctorChecks(doctorScene{tcBinary: exe, cacheDir: cacheDir, getenv: os.Getenv})
	code := 0
	for _, c := range checks {
		fmt.Fprintf(stdout, "%-4s  %-18s %s\n", c.level, c.name, c.detail)
		if c.level == doctorFail {
			code = exitCodeFailure
		}
	}
	return code
}

func collectDoctorChecks(scene doctorScene) []doctorCheck {
	return []doctorCheck{
		{doctorOK, "binary", fmt.Sprintf("%s (%s)", scene.tcBinary, version)},
		checkDisableSwitch(scene),
		checkScriptShell(scene),
		checkNodePreloadFile(scene),
		checkNodePreloadActive(scene),
		checkNodePreloadErrors(scene),
		checkRunLog(scene),
	}
}

func checkDisableSwitch(scene doctorScene) doctorCheck {
	const name = "TC_DISABLE"
	if v := scene.getenv(config.EnvDisable); v != "" && v != "0" {
		return doctorCheck{doctorWarn, name, "set: every integration is off in this shell"}
	}
	return doctorCheck{doctorOK, name, "not set"}
}

// checkScriptShell covers `pnpm test` / `npm test`: the script shell must point at a tc-shell
// that exists, or every package script fails.
func checkScriptShell(scene doctorScene) doctorCheck {
	const name = "script shell"
	shell := scene.getenv(envScriptShellPnpmRT)
	if shell == "" {
		shell = scene.getenv(envScriptShellNpmRT)
	}
	if shell == "" {
		return doctorCheck{doctorWarn, name, notActiveInShell}
	}
	if _, err := os.Stat(shell); err != nil {
		return doctorCheck{doctorFail, name, fmt.Sprintf("%s is configured but missing: every package script fails; %s", shell, rerunInstaller)}
	}
	return doctorCheck{doctorOK, name, shell}
}

// checkNodePreloadFile compares the installed preload with the one this binary carries.
func checkNodePreloadFile(scene doctorScene) doctorCheck {
	const name = "node preload file"
	path := integrate.NodePreloadPath(scene.tcBinary)
	installed, err := integrate.ReadInstalledNodePreload(scene.tcBinary)
	if err != nil {
		return doctorCheck{doctorWarn, name, fmt.Sprintf("%s is not installed; %s", path, rerunInstaller)}
	}
	if !bytes.Equal(installed, integrate.NodePreloadSource()) {
		return doctorCheck{doctorWarn, name, fmt.Sprintf("%s differs from this tc (older install or uninstalled stub); %s", path, rerunInstaller)}
	}
	return doctorCheck{doctorOK, name, path}
}

// checkNodePreloadActive looks at NODE_OPTIONS: a --require of a file that does not exist
// makes every Node process of the shell fail, which is the one state that must never last.
func checkNodePreloadActive(scene doctorScene) doctorCheck {
	const name = "node preload"
	options := scene.getenv(integrate.EnvNodeOptions)
	path := integrate.NodePreloadPath(scene.tcBinary)
	if named := scene.getenv(integrate.EnvNodePreloadPath); named != "" {
		path = named
	}
	if !strings.Contains(options, path) {
		return doctorCheck{doctorWarn, name, "NODE_OPTIONS does not load it: " + notActiveInShell}
	}
	if _, err := os.Stat(path); err != nil {
		return doctorCheck{doctorFail, name, fmt.Sprintf("NODE_OPTIONS loads %s, which is missing: every Node process fails; %s", path, rerunInstaller)}
	}
	return doctorCheck{doctorOK, name, "NODE_OPTIONS loads " + path}
}

// checkNodePreloadErrors surfaces the runs the preload could not hand to tc.
func checkNodePreloadErrors(scene doctorScene) doctorCheck {
	const name = "preload errors"
	logPath := filepath.Join(scene.cacheDir, integrate.NodePreloadErrorLog)
	data, err := os.ReadFile(logPath)
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if err != nil || len(data) == 0 {
		return doctorCheck{doctorOK, name, "none logged"}
	}
	shown := lines
	if len(shown) > doctorShownErrors {
		shown = shown[len(shown)-doctorShownErrors:]
	}
	detail := fmt.Sprintf("%d logged in %s; latest:\n        %s", len(lines), logPath, strings.Join(shown, "\n        "))
	return doctorCheck{doctorWarn, name, detail}
}

// checkRunLog reports the last run and how many recent runs hit a failure of tc itself.
func checkRunLog(scene doctorScene) doctorCheck {
	const name = "run log"
	runs, err := runlog.OpenLog(scene.cacheDir).ReadAll()
	if err != nil {
		return doctorCheck{doctorWarn, name, fmt.Sprintf("unreadable: %v", err)}
	}
	if len(runs) == 0 {
		return doctorCheck{doctorWarn, name, "empty: no test run has gone through tc yet"}
	}
	if len(runs) > doctorRecentRuns {
		runs = runs[len(runs)-doctorRecentRuns:]
	}
	failed, byInvoker := 0, map[runlog.Invoker]int{}
	for _, r := range runs {
		byInvoker[r.Record.InvokedBy]++
		if r.Record.Error != "" || r.Record.HasAnomaly(runlog.AnomalyInternalError) {
			failed++
		}
	}
	last := runs[len(runs)-1].Record
	detail := fmt.Sprintf("last run %s via %s; last %d runs: %s", last.Time, last.InvokedBy, len(runs), formatInvokerCounts(byInvoker))
	if failed > 0 {
		return doctorCheck{doctorWarn, name, fmt.Sprintf("%s; %d with a tc error (see `tc log --anomalies`)", detail, failed)}
	}
	return doctorCheck{doctorOK, name, detail}
}

func formatInvokerCounts(counts map[runlog.Invoker]int) string {
	var parts []string
	for _, who := range []runlog.Invoker{runlog.InvokerNodePreload, runlog.InvokerScriptShell, runlog.InvokerClaudeHook, runlog.InvokerDirect} {
		if n := counts[who]; n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", n, who))
		}
	}
	return strings.Join(parts, ", ")
}
