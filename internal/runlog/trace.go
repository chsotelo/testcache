package runlog

import (
	"sync"
	"time"
)

// Trace collects what one invocation did while it runs. Every method is safe on a nil
// receiver and from several goroutines: the runner may still be working when tc gives up.
type Trace struct {
	mu  sync.Mutex
	rec Record
}

// NewTrace starts a trace from the fields known before the run.
func NewTrace(initial Record) *Trace {
	if initial.Anomalies == nil {
		initial.Anomalies = []Anomaly{}
	}
	return &Trace{rec: initial}
}

// SetRepo records the repository the runner resolved.
func (t *Trace) SetRepo(root, id string) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.rec.RepoRoot, t.rec.RepoID = root, id
}

// SetMode records how the invocation is being served.
func (t *Trace) SetMode(mode Mode) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.rec.Mode = mode
}

// SetFiles records the file counts known once the plan is settled; Uncacheable is kept.
func (t *Trace) SetFiles(counts FileCounts) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	counts.Uncacheable = t.rec.Files.Uncacheable
	t.rec.Files = counts
}

// AddUncacheable counts passing files whose result could not be stored.
func (t *Trace) AddUncacheable(n int) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.rec.Files.Uncacheable += n
}

// AddPhase adds elapsed time to a phase.
func (t *Trace) AddPhase(phase Phase, elapsed time.Duration) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	ms := elapsed.Milliseconds()
	p := &t.rec.PhasesMs
	switch phase {
	case PhaseDiscover:
		p.Discover += ms
	case PhaseHashing:
		p.Hashing += ms
	case PhaseLookup:
		p.Lookup += ms
	case PhaseLockWait:
		p.LockWait += ms
	case PhaseRun:
		p.Run += ms
	case PhasePersist:
		p.Persist += ms
	case PhaseGC:
		p.GC += ms
	}
}

// AddAnomaly flags the run once per code; the first detail wins.
func (t *Trace) AddAnomaly(code AnomalyCode, detail string) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.rec.HasAnomaly(code) {
		return
	}
	t.rec.Anomalies = append(t.rec.Anomalies, Anomaly{Code: code, Detail: detail})
}

// SetVitestExit records the exit code of the Vitest process.
func (t *Trace) SetVitestExit(code int) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.rec.VitestExit = &code
}

// SetSavedMs records the test time replayed from the cache.
func (t *Trace) SetSavedMs(savedMs int64) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.rec.SavedS = float64(savedMs) / 1000
}

// CopyRecord returns the record as collected so far.
func (t *Trace) CopyRecord() Record {
	if t == nil {
		return Record{Anomalies: []Anomaly{}}
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	out := t.rec
	out.Anomalies = append([]Anomaly{}, t.rec.Anomalies...)
	return out
}
