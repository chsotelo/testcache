package runlog

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"

	"github.com/chsotelo/testcache/internal/lock"
)

const (
	// FileName is the run log inside the cache root.
	FileName     = "runs.jsonl"
	lockFileName = "runs.lock"
	// DefaultMaxBytes is the size past which the log rotates.
	DefaultMaxBytes int64 = 10 << 20
	// rotatedFiles is how many older files are kept beside the live one: 3 files in total.
	rotatedFiles = 2
	// maxLineBytes keeps one line far below what a single write appends atomically.
	maxLineBytes = 32 << 10
	maxArgvItems = 16
	maxArgBytes  = 256
	// tailBytes is how much of the live file anomaly detection reads.
	tailBytes int64 = 512 << 10
	// historyLimit is how many runs of one repository anomaly detection looks back on.
	historyLimit = 200
)

// Log is the JSONL run log stored in Dir.
type Log struct {
	Dir      string
	MaxBytes int64
}

// LoggedRun is one parsed line together with its raw text.
type LoggedRun struct {
	Record Record
	Raw    []byte
}

// OpenLog returns the run log of the cache root baseDir.
func OpenLog(baseDir string) Log {
	return Log{Dir: baseDir, MaxBytes: DefaultMaxBytes}
}

func (l Log) livePath() string {
	return filepath.Join(l.Dir, FileName)
}

func (l Log) rotatedPath(n int) string {
	return fmt.Sprintf("%s.%d", l.livePath(), n)
}

// EncodeLine renders rec as one newline-terminated JSON line, trimming argv when it is huge.
func EncodeLine(rec Record) ([]byte, error) {
	if rec.Anomalies == nil {
		rec.Anomalies = []Anomaly{}
	}
	line, err := json.Marshal(rec)
	if err != nil {
		return nil, fmt.Errorf("encode run record: %w", err)
	}
	if len(line) > maxLineBytes {
		rec.Argv = trimArgv(rec.Argv)
		rec.Error = cutText(rec.Error, maxArgBytes)
		if line, err = json.Marshal(rec); err != nil {
			return nil, fmt.Errorf("encode run record: %w", err)
		}
	}
	return append(line, '\n'), nil
}

func trimArgv(argv []string) []string {
	out := make([]string, 0, maxArgvItems+1)
	for i, a := range argv {
		if i == maxArgvItems {
			out = append(out, fmt.Sprintf("...(+%d)", len(argv)-maxArgvItems))
			break
		}
		out = append(out, cutText(a, maxArgBytes))
	}
	return out
}

func cutText(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	return s[:limit] + "..."
}

// AppendLine adds one line to the log with a single O_APPEND write, rotating first when the
// live file is over MaxBytes. Appends and rotations are serialized by a kernel flock.
func (l Log) AppendLine(line []byte) error {
	if err := os.MkdirAll(l.Dir, 0o755); err != nil {
		return fmt.Errorf("create run log dir: %w", err)
	}
	guard, err := lock.Acquire(filepath.Join(l.Dir, lockFileName), nil)
	if err != nil {
		return fmt.Errorf("lock run log: %w", err)
	}
	defer guard.Release()

	if info, err := os.Stat(l.livePath()); err == nil && info.Size() > l.MaxBytes {
		l.rotateFiles()
	}
	f, err := os.OpenFile(l.livePath(), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open run log: %w", err)
	}
	if _, err := f.Write(line); err != nil {
		_ = f.Close()
		return fmt.Errorf("append run log: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close run log: %w", err)
	}
	return nil
}

// rotateFiles shifts runs.jsonl -> .1 -> .2 and drops the oldest. Errors leave the live file
// growing, which the next append retries.
func (l Log) rotateFiles() {
	_ = os.Remove(l.rotatedPath(rotatedFiles))
	for n := rotatedFiles - 1; n >= 1; n-- {
		_ = os.Rename(l.rotatedPath(n), l.rotatedPath(n+1))
	}
	_ = os.Rename(l.livePath(), l.rotatedPath(1))
}

// ReadRepoHistory returns the most recent runs of one repository, oldest first. It reads at
// most the last 512 KiB of the live file and decodes only that repository's lines, newest
// first, until historyLimit of them are found: cheap enough for every run to do.
func (l Log) ReadRepoHistory(repoID string) ([]Record, error) {
	f, err := os.Open(l.livePath())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("open run log: %w", err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("stat run log: %w", err)
	}
	offset := max(info.Size()-tailBytes, 0)
	data, err := io.ReadAll(io.NewSectionReader(f, offset, info.Size()-offset))
	if err != nil {
		return nil, fmt.Errorf("read run log: %w", err)
	}
	marker, err := json.Marshal(repoID)
	if err != nil {
		return nil, fmt.Errorf("encode repo id: %w", err)
	}
	lines := bytes.Split(data, []byte("\n"))
	var out []Record
	for i := len(lines) - 1; i >= 0 && len(out) < historyLimit; i-- {
		// A line cut in half by the offset, or not of this repository, is skipped.
		if !bytes.Contains(lines[i], marker) {
			continue
		}
		var rec Record
		if json.Unmarshal(lines[i], &rec) == nil && rec.RepoID == repoID {
			out = append(out, rec)
		}
	}
	slices.Reverse(out)
	return out, nil
}

// ReadAll returns every run still on disk, oldest first, across the rotated files.
func (l Log) ReadAll() ([]LoggedRun, error) {
	var out []LoggedRun
	paths := make([]string, 0, rotatedFiles+1)
	for n := rotatedFiles; n >= 1; n-- {
		paths = append(paths, l.rotatedPath(n))
	}
	for _, p := range append(paths, l.livePath()) {
		data, err := os.ReadFile(p)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return nil, fmt.Errorf("read run log: %w", err)
		}
		out = append(out, parseLines(data)...)
	}
	return out, nil
}

// parseLines decodes JSONL, skipping blank and unparsable lines.
func parseLines(data []byte) []LoggedRun {
	var out []LoggedRun
	for _, raw := range bytes.Split(data, []byte("\n")) {
		if len(bytes.TrimSpace(raw)) == 0 {
			continue
		}
		var rec Record
		if err := json.Unmarshal(raw, &rec); err != nil {
			continue
		}
		out = append(out, LoggedRun{Record: rec, Raw: raw})
	}
	return out
}
