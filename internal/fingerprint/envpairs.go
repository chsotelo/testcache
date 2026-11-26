package fingerprint

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"sort"
	"strings"
)

const (
	// SystemTimezoneKey is the pseudo env key carrying the system time zone in cache keys.
	SystemTimezoneKey = "system:localtime"
	localtimePath     = "/etc/localtime"
	// EnvNamesKey is the pseudo env key recorded for a test that lists the environment
	// (Object.keys(process.env), for...in, a spread). Its value is the sorted list of the
	// names of every variable: such a test depends on which variables exist, not only on the
	// values it goes on to read.
	EnvNamesKey       = "tc:env-names"
	envNamesSeparator = ","
	internalEnvPrefix = "TESTCACHE_"
)

// TCOwnEnvKeys are set by tc and its integrations for themselves. They are removed from the
// environment of the Vitest tc starts (or constant there), so a test never sees them vary.
func TCOwnEnvKeys() []string {
	return []string{"TC_INVOKED_BY", "TC_NODE_PRELOAD_ACTIVE", "TC_DISABLE"}
}

// ReadEnvNames returns the names of the variables a test started by tc can list, sorted and
// joined: everything in this process's environment except what tc itself sets.
func ReadEnvNames() string {
	own := map[string]struct{}{}
	for _, k := range TCOwnEnvKeys() {
		own[k] = struct{}{}
	}
	var names []string
	for _, entry := range os.Environ() {
		name, _, ok := strings.Cut(entry, "=")
		if _, mine := own[name]; !ok || mine || name == "" || strings.HasPrefix(name, internalEnvPrefix) {
			continue
		}
		names = append(names, name)
	}
	sort.Strings(names)
	return strings.Join(names, envNamesSeparator)
}

// LookupEnvValue reads one recorded key: a real variable, or the list of names for EnvNamesKey.
func LookupEnvValue(key string) (string, bool) {
	if key == EnvNamesKey {
		return ReadEnvNames(), true
	}
	return os.LookupEnv(key)
}

// DescribeEnvNamesChange lists the names added to and removed from a recorded EnvNamesKey value.
func DescribeEnvNamesChange(oldValue, curValue string) string {
	old := map[string]struct{}{}
	for _, n := range strings.Split(oldValue, envNamesSeparator) {
		old[n] = struct{}{}
	}
	var parts []string
	for _, n := range strings.Split(curValue, envNamesSeparator) {
		if _, ok := old[n]; !ok {
			parts = append(parts, "+"+n)
		}
		delete(old, n)
	}
	removed := make([]string, 0, len(old))
	for n := range old {
		removed = append(removed, "-"+n)
	}
	sort.Strings(removed)
	return strings.Join(append(parts, removed...), " ")
}

// CollectAllowlistedEnv reads the variables every test result depends on: NODE_ENV, CI, the
// time zone and locale (TZ, LANG, LC_ALL, LC_MESSAGES and the system zone used when TZ is
// unset), every VITE_* and VITEST_* variable, and any extra keys.
func CollectAllowlistedEnv(extraKeys []string) []EnvPair {
	keys := map[string]struct{}{}
	for _, k := range []string{"NODE_ENV", "CI", "TZ", "LANG", "LC_ALL", "LC_MESSAGES"} {
		keys[k] = struct{}{}
	}
	for _, k := range extraKeys {
		if k != "" {
			keys[k] = struct{}{}
		}
	}
	for _, entry := range os.Environ() {
		name, _, ok := strings.Cut(entry, "=")
		if ok && (strings.HasPrefix(name, "VITEST_") || strings.HasPrefix(name, "VITE_")) {
			keys[name] = struct{}{}
		}
	}
	pairs := make([]EnvPair, 0, len(keys)+1)
	for k := range keys {
		v, ok := os.LookupEnv(k)
		pairs = append(pairs, EnvPair{Key: k, Value: v, Set: ok})
	}
	zone := ReadSystemTimezone(localtimePath)
	pairs = append(pairs, EnvPair{Key: SystemTimezoneKey, Value: zone, Set: zone != ""})
	sort.Slice(pairs, func(i, j int) bool { return pairs[i].Key < pairs[j].Key })
	return pairs
}

// ReadSystemTimezone identifies the zone behind a localtime file: the link target when it is a
// symlink (macOS, most Linux), otherwise a hash of the zone data. It returns "" when unreadable.
func ReadSystemTimezone(path string) string {
	if target, err := os.Readlink(path); err == nil {
		return target
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(data)
	return rawHashPrefix + hex.EncodeToString(sum[:])
}
