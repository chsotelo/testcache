package fingerprint

import (
	"os"
	"path/filepath"
	"testing"
)

func hashMarker(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), ".modules.yaml")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	digest, ok := HashInstallMarker(path)
	if !ok {
		t.Fatal("marker must be readable")
	}
	return digest
}

// Two installs of one lockfile differ in timestamp and key order only.
func TestHashInstallMarkerIgnoresTimestampAndOrder(t *testing.T) {
	first := `{"hoistedDependencies":{"a@1.0.0":{"a":"private"},"b@2.0.0":{"b":"private"}},"layoutVersion":5,"prunedAt":"Mon, 23 Nov 2026 22:52:54 GMT"}`
	again := `{"layoutVersion":5,"prunedAt":"Tue, 24 Nov 2026 01:00:00 GMT","hoistedDependencies":{"b@2.0.0":{"b":"private"},"a@1.0.0":{"a":"private"}}}`
	other := `{"hoistedDependencies":{"a@1.0.1":{"a":"private"},"b@2.0.0":{"b":"private"}},"layoutVersion":5,"prunedAt":"Mon, 23 Nov 2026 22:52:54 GMT"}`
	if hashMarker(t, first) != hashMarker(t, again) {
		t.Fatal("the same installation must hash the same")
	}
	if hashMarker(t, first) == hashMarker(t, other) {
		t.Fatal("another installed version must hash differently")
	}
}

func TestHashInstallMarkerYAML(t *testing.T) {
	first := "layoutVersion: 5\nprunedAt: 'Mon, 23 Nov 2026 22:52:54 GMT'\npackageManager: pnpm@9.0.0\n"
	again := "layoutVersion: 5\nprunedAt: 'Tue, 24 Nov 2026 01:00:00 GMT'\npackageManager: pnpm@9.0.0\n"
	other := "layoutVersion: 5\nprunedAt: 'Mon, 23 Nov 2026 22:52:54 GMT'\npackageManager: pnpm@9.1.0\n"
	if hashMarker(t, first) != hashMarker(t, again) || hashMarker(t, first) == hashMarker(t, other) {
		t.Fatal("a YAML marker must ignore the timestamp line and nothing else")
	}
	if _, ok := HashInstallMarker(filepath.Join(t.TempDir(), "missing")); ok {
		t.Fatal("a missing marker is left to the caller")
	}
}
