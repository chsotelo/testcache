// Package runlog keeps the global JSONL log of tc runs and flags the anomalous ones.
package runlog

import "os"

// AnomalyCode names one kind of run that deserves a second look.
type AnomalyCode string

const (
	// AnomalySlowRun marks a run far slower than recent similar runs of the same repo.
	AnomalySlowRun AnomalyCode = "slow_run"
	// AnomalySlowHitPath marks a run that executed nothing and still took seconds.
	AnomalySlowHitPath AnomalyCode = "slow_hit_path"
	// AnomalyLongLockWait marks a run that queued behind another tc for over a minute.
	AnomalyLongLockWait AnomalyCode = "long_lock_wait"
	// AnomalyStaleEntry marks a cached pass that failed on re-run with identical inputs.
	AnomalyStaleEntry AnomalyCode = "stale_entry"
	// AnomalyQuarantined marks a run that included files already under quarantine.
	AnomalyQuarantined AnomalyCode = "quarantined"
	// AnomalyInterrupted marks a run stopped by SIGINT or SIGTERM.
	AnomalyInterrupted AnomalyCode = "interrupted"
	// AnomalyInternalError marks a failure of tc itself: a panic, I/O error or corrupt cache data.
	AnomalyInternalError AnomalyCode = "internal_error"
	// AnomalyNodeVersionMismatch marks a run where Vitest ran on another Node than the cache keys assume.
	AnomalyNodeVersionMismatch AnomalyCode = "node_version_mismatch"
)

// Invoker says which path started tc.
type Invoker string

const (
	// EnvInvokedBy is set by the integrations so the log can tell them apart.
	EnvInvokedBy = "TC_INVOKED_BY"
	// InvokerDirect is a tc typed or scripted by hand.
	InvokerDirect Invoker = "direct"
	// InvokerScriptShell is a package script rewritten by the npm/pnpm script shell.
	InvokerScriptShell Invoker = "script-shell"
	// InvokerClaudeHook is a Bash command rewritten by the Claude Code hook.
	InvokerClaudeHook Invoker = "claude-hook"
	// InvokerNodePreload is a Vitest CLI process handed to tc by the Node preload.
	InvokerNodePreload Invoker = "node-preload"
)

// Mode says how the invocation was served.
type Mode string

const (
	// ModeCache is a normal cached `tc vitest` run.
	ModeCache Mode = "cache"
	// ModeVerify is a `tc verify` run.
	ModeVerify Mode = "verify"
	// ModePassthrough is a run handed to Vitest untouched (watch, UI, no test files).
	ModePassthrough Mode = "passthrough"
	// ModeDisabled is a plain Vitest run because TC_DISABLE was set.
	ModeDisabled Mode = "disabled"
	// ModeFallback is a plain Vitest run after tc itself panicked.
	ModeFallback Mode = "fallback"
)

// Phase names one timed step of a run.
type Phase string

// The phases of a run, in order.
const (
	PhaseDiscover Phase = "discover"
	PhaseHashing  Phase = "hashing"
	PhaseLookup   Phase = "lookup"
	PhaseLockWait Phase = "lock_wait"
	PhaseRun      Phase = "run"
	PhasePersist  Phase = "persist"
	PhaseGC       Phase = "gc"
)

// Anomaly is one flagged condition of a run.
type Anomaly struct {
	Code   AnomalyCode `json:"code"`
	Detail string      `json:"detail,omitempty"`
}

// FileCounts says what happened to the test files of a run.
type FileCounts struct {
	Discovered  int `json:"discovered"`
	Cached      int `json:"cached"`
	Run         int `json:"run"`
	Uncacheable int `json:"uncacheable"`
	Quarantined int `json:"quarantined"`
}

// PhaseDurations holds the milliseconds spent in each phase.
type PhaseDurations struct {
	Discover int64 `json:"discover"`
	Hashing  int64 `json:"hashing"`
	Lookup   int64 `json:"lookup"`
	LockWait int64 `json:"lock_wait"`
	Run      int64 `json:"run"`
	Persist  int64 `json:"persist"`
	GC       int64 `json:"gc"`
}

// Record is one line of runs.jsonl.
type Record struct {
	Time       string         `json:"time"`
	TCVersion  string         `json:"tc_version"`
	Command    string         `json:"command"`
	Mode       Mode           `json:"mode"`
	InvokedBy  Invoker        `json:"invoked_by"`
	RepoRoot   string         `json:"repo_root"`
	RepoID     string         `json:"repo_id"`
	Cwd        string         `json:"cwd"`
	Argv       []string       `json:"argv"`
	Files      FileCounts     `json:"files"`
	PhasesMs   PhaseDurations `json:"phases_ms"`
	DurationMs int64          `json:"duration_ms"`
	VitestExit *int           `json:"vitest_exit"`
	TCExit     int            `json:"tc_exit"`
	SavedS     float64        `json:"saved_s"`
	Anomalies  []Anomaly      `json:"anomalies"`
	Error      string         `json:"error,omitempty"`
}

// ReadInvoker returns who started tc according to TC_INVOKED_BY; unknown values count as direct.
func ReadInvoker() Invoker {
	switch v := Invoker(os.Getenv(EnvInvokedBy)); v {
	case InvokerScriptShell, InvokerClaudeHook, InvokerNodePreload:
		return v
	default:
		return InvokerDirect
	}
}

// HasAnomaly reports whether the record carries the given code.
func (r Record) HasAnomaly(code AnomalyCode) bool {
	for _, a := range r.Anomalies {
		if a.Code == code {
			return true
		}
	}
	return false
}
