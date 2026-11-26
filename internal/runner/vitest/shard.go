package vitest

import (
	"crypto/sha1" //nolint:gosec // Vitest's shard order, not a security boundary.
	"encoding/hex"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// shardSpec is `--shard <index>/<count>`: run the index-th of count parts of the selected files.
type shardSpec struct {
	index, count int
}

func (s shardSpec) active() bool { return s.count > 0 }

func (s shardSpec) String() string { return fmt.Sprintf("%d/%d", s.index, s.count) }

// parseShard accepts what Vitest accepts: two positive integers, index no greater than count.
func parseShard(val string) (shardSpec, bool) {
	idx, cnt, ok := strings.Cut(val, "/")
	if !ok {
		return shardSpec{}, false
	}
	index, err1 := strconv.Atoi(idx)
	count, err2 := strconv.Atoi(cnt)
	if err1 != nil || err2 != nil || index < 1 || count < 1 || index > count {
		return shardSpec{}, false
	}
	return shardSpec{index: index, count: count}, true
}

// ownedByShard returns the files of shard s among files, cut the way Vitest's default
// sequencer cuts them: sorted by the SHA-1 of their path from the project root, then split
// into count consecutive ranges. Every shard of a run discovers the same files and applies
// the same cut, whatever each one has cached, so together they cover each file exactly once
// and each holds the files it would hold without tc. ok is false when Vitest would refuse
// the split (more shards than files), which tc then leaves to Vitest.
func ownedByShard(files []string, projRoot string, s shardSpec, vitestVersion string) (owned []string, ok bool) {
	if s.count > len(files) {
		return nil, false
	}
	type keyed struct{ file, hash string }
	all := make([]keyed, 0, len(files))
	for _, f := range files {
		sum := sha1.Sum([]byte("/" + relSlashPath(projRoot, f))) //nolint:gosec // see import.
		all = append(all, keyed{file: f, hash: hex.EncodeToString(sum[:])})
	}
	sort.Slice(all, func(i, j int) bool { return all[i].hash < all[j].hash })
	start, end := shardRange(len(all), s, vitestMajor(vitestVersion))
	for _, k := range all[start:end] {
		owned = append(owned, k.file)
	}
	return owned, true
}

// shardRange is calculateShardRange of Vitest 4 and later, which spreads the remainder over
// the first shards, and the equal ceil(n/count) ranges of Vitest 3.
func shardRange(n int, s shardSpec, major int) (start, end int) {
	if major > 0 && major < 4 {
		size := (n + s.count - 1) / s.count
		return min(size*(s.index-1), n), min(size*s.index, n)
	}
	base, rem := n/s.count, n%s.count
	if rem >= s.index {
		return (base + 1) * (s.index - 1), (base + 1) * s.index
	}
	start = rem*(base+1) + (s.index-rem-1)*base
	return start, start + base
}

func vitestMajor(version string) int {
	major, _, _ := strings.Cut(version, ".")
	n, err := strconv.Atoi(major)
	if err != nil {
		return 0
	}
	return n
}

// applyShard keeps the selected files that belong to the shard. Files outside it are neither
// replayed nor run here: another shard owns them.
func applyShard(disc DiscoveryResult, projRoot string, s shardSpec, vitestVersion string) DiscoveryResult {
	all := append(append([]string(nil), disc.Cacheable...), disc.ExcludedByTC...)
	owned, ok := ownedByShard(all, projRoot, s, vitestVersion)
	if !ok {
		disc.Cacheable, disc.ExcludedByTC = nil, nil
		return disc
	}
	mine := make(map[string]struct{}, len(owned))
	for _, f := range owned {
		mine[f] = struct{}{}
	}
	disc.Cacheable = keepOwned(disc.Cacheable, mine)
	disc.ExcludedByTC = keepOwned(disc.ExcludedByTC, mine)
	return disc
}

func keepOwned(files []string, mine map[string]struct{}) []string {
	var out []string
	for _, f := range files {
		if _, ok := mine[f]; ok {
			out = append(out, f)
		}
	}
	return out
}
