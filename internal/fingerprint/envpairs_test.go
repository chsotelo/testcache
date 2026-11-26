package fingerprint

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadSystemTimezone(t *testing.T) {
	dir := t.TempDir()
	zoneA := filepath.Join(dir, "Europe", "Berlin")
	if err := os.MkdirAll(filepath.Dir(zoneA), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(zoneA, []byte("TZif-berlin"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "localtime")
	if err := os.Symlink(zoneA, link); err != nil {
		t.Fatal(err)
	}
	if got := ReadSystemTimezone(link); got != zoneA {
		t.Fatalf("symlinked localtime = %q, want its target %q", got, zoneA)
	}

	copied := filepath.Join(dir, "localtime-copy")
	if err := os.WriteFile(copied, []byte("TZif-berlin"), 0o644); err != nil {
		t.Fatal(err)
	}
	first := ReadSystemTimezone(copied)
	if !strings.HasPrefix(first, rawHashPrefix) {
		t.Fatalf("copied localtime = %q, want a content hash", first)
	}
	if err := os.WriteFile(copied, []byte("TZif-madrid"), 0o644); err != nil {
		t.Fatal(err)
	}
	if ReadSystemTimezone(copied) == first {
		t.Fatalf("a different zone file must give a different identity")
	}
	if got := ReadSystemTimezone(filepath.Join(dir, "absent")); got != "" {
		t.Fatalf("missing localtime = %q, want empty", got)
	}
}

func TestCollectAllowlistedEnvCoversLocaleAndVitePrefix(t *testing.T) {
	t.Setenv("LANG", "es_PE.UTF-8")
	t.Setenv("VITE_TC_UNIT", "1")
	got := make(map[string]EnvPair)
	for _, p := range CollectAllowlistedEnv([]string{"EXTRA_KEY"}) {
		got[p.Key] = p
	}
	for _, key := range []string{"NODE_ENV", "CI", "TZ", "LANG", "LC_ALL", "LC_MESSAGES", "VITE_TC_UNIT", "EXTRA_KEY", SystemTimezoneKey} {
		if _, ok := got[key]; !ok {
			t.Errorf("allowlist is missing %s", key)
		}
	}
	if got["LANG"].Value != "es_PE.UTF-8" || !got["VITE_TC_UNIT"].Set {
		t.Fatalf("unexpected values: %+v %+v", got["LANG"], got["VITE_TC_UNIT"])
	}
}
