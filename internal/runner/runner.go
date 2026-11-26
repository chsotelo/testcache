package runner

import (
	"context"
	"errors"
	"io"

	"github.com/chsotelo/testcache/internal/runlog"
)

// RunOptions configures a single runner invocation.
type RunOptions struct {
	WorkDir      string
	Args         []string
	NoCache      bool
	NoRevalidate bool
	RandFloat64  func() float64
	Stdout       io.Writer
	Stderr       io.Writer
	// Trace, when set, collects what the run did for the run log.
	Trace *runlog.Trace
}

// Runner executes a test suite with content-addressed caching.
type Runner interface {
	Name() string
	RunTests(ctx context.Context, opts RunOptions) (int, error)
}

// usageError marks a failure caused by how tc was called, not by tc itself.
type usageError struct{ cause error }

func (e usageError) Error() string { return e.cause.Error() }

func (e usageError) Unwrap() error { return e.cause }

// MarkUsageError wraps err so the run log does not report it as an internal error.
func MarkUsageError(err error) error {
	if err == nil {
		return nil
	}
	return usageError{cause: err}
}

// IsUsageError reports whether err was marked with MarkUsageError.
func IsUsageError(err error) bool {
	var target usageError
	return errors.As(err, &target)
}
