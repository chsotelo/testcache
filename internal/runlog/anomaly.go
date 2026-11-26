package runlog

import (
	"fmt"
	"sort"
	"time"
)

const (
	// slowRunFloor is the least a run must take to be called slow, whatever its history.
	slowRunFloor = 120 * time.Second
	// slowRunFactor is how many times the median a slow run exceeds.
	slowRunFactor = 3
	// slowRunWindow is how many recent similar runs form the baseline.
	slowRunWindow = 20
	// slowRunMinSamples is the least history a baseline needs.
	slowRunMinSamples = 5
	// slowHitPathLimit is the most a run that executes nothing may take, lock waits aside.
	slowHitPathLimit = 5 * time.Second
	// longLockWaitLimit is the longest a run may queue behind another tc.
	longLockWaitLimit = 60 * time.Second
)

// DetectAnomalies returns the timing anomalies of rec given the earlier runs in the log
// (oldest first). The codes raised during the run itself are already on the record.
func DetectAnomalies(rec Record, history []Record) []Anomaly {
	var out []Anomaly
	if wait := msToDuration(rec.PhasesMs.LockWait); wait > longLockWaitLimit {
		out = append(out, Anomaly{Code: AnomalyLongLockWait, Detail: fmt.Sprintf("waited %s for another tc run", roundDuration(wait))})
	}
	if rec.Mode != ModeCache && rec.Mode != ModeVerify {
		return out
	}
	total := msToDuration(rec.DurationMs)
	if busy := total - msToDuration(rec.PhasesMs.LockWait); isHitOnlyRun(rec) && busy > slowHitPathLimit {
		out = append(out, Anomaly{Code: AnomalySlowHitPath, Detail: fmt.Sprintf("took %s with 0 files run", roundDuration(busy))})
	}
	if median, ok := findBaselineMedian(rec, history); ok {
		limit := max(slowRunFactor*median, slowRunFloor)
		if total > limit {
			out = append(out, Anomaly{Code: AnomalySlowRun, Detail: fmt.Sprintf("took %s; median of similar runs is %s", roundDuration(total), roundDuration(median))})
		}
	}
	return out
}

func isHitOnlyRun(rec Record) bool {
	return rec.Mode == ModeCache && rec.Files.Run == 0 && rec.Files.Discovered > 0 && rec.Error == ""
}

// findBaselineMedian returns the median duration of the last slowRunWindow successful runs of
// the same repo and mode that ran a similar number of files.
func findBaselineMedian(rec Record, history []Record) (time.Duration, bool) {
	var samples []int64
	for i := len(history) - 1; i >= 0 && len(samples) < slowRunWindow; i-- {
		h := history[i]
		if h.RepoID != rec.RepoID || h.Mode != rec.Mode || h.TCExit != 0 || h.Error != "" {
			continue
		}
		if !isSimilarFileCount(h.Files.Run, rec.Files.Run) {
			continue
		}
		samples = append(samples, h.DurationMs)
	}
	if len(samples) < slowRunMinSamples {
		return 0, false
	}
	sort.Slice(samples, func(i, j int) bool { return samples[i] < samples[j] })
	mid := len(samples) / 2
	median := samples[mid]
	if len(samples)%2 == 0 {
		median = (samples[mid-1] + samples[mid]) / 2
	}
	return msToDuration(median), true
}

// isSimilarFileCount reports whether two runs executed about as many files: within a factor
// of two, give or take two files.
func isSimilarFileCount(a, b int) bool {
	lo, hi := min(a, b), max(a, b)
	return hi <= 2*lo+2
}

func msToDuration(ms int64) time.Duration {
	return time.Duration(ms) * time.Millisecond
}

func roundDuration(d time.Duration) time.Duration {
	if d >= time.Second {
		return d.Round(100 * time.Millisecond)
	}
	return d.Round(time.Millisecond)
}

// ShouldAlert reports whether rec carries an anomaly worth alerting on: any but interrupted.
func ShouldAlert(rec Record) bool {
	for _, a := range rec.Anomalies {
		if a.Code != AnomalyInterrupted {
			return true
		}
	}
	return false
}
