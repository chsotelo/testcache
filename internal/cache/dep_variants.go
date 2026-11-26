package cache

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

// DepVariant is one observed set of inputs of a test file: what a passing run executed, read
// and listed. An entry stored under a variant stays valid whatever other variants exist: it
// says "with these inputs at these contents, the test passed".
type DepVariant struct {
	Deps         []string  `json:"deps"`
	Globs        []GlobDep `json:"globs,omitempty"`
	EnvVars      []string  `json:"envVars,omitempty"`
	FallbackDeps bool      `json:"fallbackDeps"`
}

// maxOlderDepVariants bounds the lists kept besides the newest one.
const maxOlderDepVariants = 7

// Variants returns every known dependency list, the newest first.
func (r *DepRecord) Variants() []DepVariant {
	out := make([]DepVariant, 0, len(r.Older)+1)
	out = append(out, r.primaryVariant())
	return append(out, r.Older...)
}

func (r *DepRecord) primaryVariant() DepVariant {
	return DepVariant{Deps: r.Deps, Globs: r.Globs, EnvVars: r.EnvVars, FallbackDeps: r.FallbackDeps}
}

func variantSignature(v DepVariant) string {
	data, _ := json.Marshal(v)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// keepOlderVariants returns the lists to keep next to a new record: the previous newest one
// and its older ones, without the list the new record carries.
func keepOlderVariants(previous *DepRecord, next DepRecord) []DepVariant {
	if previous == nil {
		return nil
	}
	seen := map[string]struct{}{variantSignature(next.primaryVariant()): {}}
	var kept []DepVariant
	for _, v := range previous.Variants() {
		sig := variantSignature(v)
		if _, dup := seen[sig]; dup || len(kept) >= maxOlderDepVariants {
			continue
		}
		seen[sig] = struct{}{}
		kept = append(kept, v)
	}
	return kept
}
