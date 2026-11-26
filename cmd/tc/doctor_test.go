package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chsotelo/testcache/internal/integrate"
)

// installedScene lays out <prefix>/bin/tc and, when asked, the preload next to it.
func installedScene(t *testing.T, preload []byte, env map[string]string) doctorScene {
	t.Helper()
	prefix := t.TempDir()
	scene := doctorScene{
		tcBinary: filepath.Join(prefix, "bin", programName),
		cacheDir: filepath.Join(prefix, "cache"),
		getenv:   func(key string) string { return env[key] },
	}
	if preload != nil {
		path := integrate.NodePreloadPath(scene.tcBinary)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, preload, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return scene
}

func TestDoctorNodePreloadFile(t *testing.T) {
	cases := map[string]struct {
		preload []byte
		want    doctorLevel
	}{
		"missing":    {nil, doctorWarn},
		"stale":      {[]byte("// older\n"), doctorWarn},
		"same as tc": {integrate.NodePreloadSource(), doctorOK},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := checkNodePreloadFile(installedScene(t, tc.preload, nil)); got.level != tc.want {
				t.Fatalf("want %s, got %+v", tc.want, got)
			}
		})
	}
}

// NODE_OPTIONS naming a preload that is gone breaks every Node process: the one hard failure.
func TestDoctorNodePreloadActive(t *testing.T) {
	inactive := installedScene(t, integrate.NodePreloadSource(), nil)
	if got := checkNodePreloadActive(inactive); got.level != doctorWarn {
		t.Fatalf("an unset NODE_OPTIONS is a warning, got %+v", got)
	}

	env := map[string]string{}
	active := installedScene(t, integrate.NodePreloadSource(), env)
	path := integrate.NodePreloadPath(active.tcBinary)
	env[integrate.EnvNodeOptions] = "--max-old-space-size=4096 --require " + path
	if got := checkNodePreloadActive(active); got.level != doctorOK {
		t.Fatalf("want ok, got %+v", got)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if got := checkNodePreloadActive(active); got.level != doctorFail || !strings.Contains(got.detail, "every Node process fails") {
		t.Fatalf("a missing preload named by NODE_OPTIONS must fail, got %+v", got)
	}
}

func TestDoctorScriptShell(t *testing.T) {
	env := map[string]string{}
	scene := installedScene(t, nil, env)
	if got := checkScriptShell(scene); got.level != doctorWarn {
		t.Fatalf("an unset script shell is a warning, got %+v", got)
	}
	env[envScriptShellPnpmRT] = filepath.Join(t.TempDir(), "gone", shellShimName)
	if got := checkScriptShell(scene); got.level != doctorFail {
		t.Fatalf("a script shell that does not exist must fail, got %+v", got)
	}
}

func TestDoctorRunLogAndErrorsWhenEmpty(t *testing.T) {
	scene := installedScene(t, nil, nil)
	if got := checkRunLog(scene); got.level != doctorWarn || !strings.Contains(got.detail, "empty") {
		t.Fatalf("want an empty-log warning, got %+v", got)
	}
	if got := checkNodePreloadErrors(scene); got.level != doctorOK {
		t.Fatalf("want ok without an error log, got %+v", got)
	}
}
