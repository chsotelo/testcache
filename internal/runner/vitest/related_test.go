package vitest

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/chsotelo/testcache/internal/cache"
)

func TestParseArgsRelated(t *testing.T) {
	p := ParseArgs([]string{"related", "--run", "--passWithNoTests", "src/a.ts", "src/b.css"})
	if !p.Related || p.BypassCache {
		t.Fatalf("related --run should be cacheable: %+v", p)
	}
	if strings.Join(p.RelatedSources, ",") != "src/a.ts,src/b.css" || len(p.Positionals) != 0 {
		t.Fatalf("sources must not become test filters: sources=%v positionals=%v", p.RelatedSources, p.Positionals)
	}
	if len(p.CacheableFlags) != 0 {
		t.Fatalf("--passWithNoTests must not key the cache: %v", p.CacheableFlags)
	}

	if !ParseArgs([]string{"related", "src/a.ts"}).BypassCache {
		t.Fatal("related without --run watches on a terminal and must be left to Vitest")
	}
	if ParseArgs([]string{"run", "related"}).Related {
		t.Fatal("a filter named related after run is a filter, not the subcommand")
	}
}

func TestRelatedSelection(t *testing.T) {
	repo := t.TempDir()
	proj := filepath.Join(repo, "web")
	hit := func(test string, deps []string, globDirs ...string) *targetState {
		v := &cache.DepVariant{Deps: deps}
		for _, d := range globDirs {
			v.Globs = append(v.Globs, cache.GlobDep{Glob: "./*.ts", BaseDir: d})
		}
		return &targetState{absPath: filepath.Join(proj, test), variant: v}
	}
	a := hit("tests/a.test.ts", []string{"web/src/a.ts"})
	b := hit("tests/b.test.ts", []string{"web/src/b.ts"}, "web/src/plugins")

	cases := []struct {
		name    string
		sources []string
		want    []string
	}{
		{"an executed file selects its test", []string{"src/a.ts"}, []string{a.absPath}},
		{"the test itself is a source", []string{"tests/b.test.ts"}, []string{b.absPath}},
		{"a file under an import.meta.glob base", []string{"src/plugins/x.ts"}, []string{b.absPath}},
		{"an absolute source", []string{filepath.Join(proj, "src", "b.ts")}, []string{b.absPath}},
		{"an unrelated file selects nothing", []string{"src/other.ts", "README.md"}, nil},
		{"package.json is a rerun trigger", []string{"package.json"}, []string{a.absPath, b.absPath}},
		{"a vite config is a rerun trigger", []string{"vite.config.ts"}, []string{a.absPath, b.absPath}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sel := newRelatedSelection(proj, tc.sources)
			var got []string
			for _, st := range sel.filter(repo, []*targetState{a, b}) {
				got = append(got, st.absPath)
			}
			if strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestVitestRelatedEndToEnd(t *testing.T) {
	projDir, _ := setupFixtureCopy(t)
	related := func(sources ...string) []string {
		return append([]string{"related", "--run", "--passWithNoTests"}, sources...)
	}

	// Cold: Vitest picks a.test.ts out of the uncached files; b.test.ts neither runs nor is stored.
	out := assertVitestSummary(t, projDir, "tc: 1 file, 0 cached, 1 run", related("src/usedByA.ts")...)
	if strings.Contains(out, "b.test.ts") {
		t.Fatalf("b.test.ts does not import usedByA.ts and must not run:\n%s", out)
	}
	assertVitestSummary(t, projDir, "tc: 2 files, 1 cached, 1 run")

	// Warm: the cached pass is replayed, and entries stored by a plain run serve related runs.
	assertVitestSummary(t, projDir, "tc: 1 file, 1 cached, 0 run", related("src/usedByA.ts")...)
	assertVitestSummary(t, projDir, "tc: 1 file, 1 cached, 0 run", related("src/usedByB.ts")...)

	// a.test.ts loses its cached pass but is unrelated to usedByB.ts: Vitest gets it and skips it.
	writeFixtureFile(t, filepath.Join(projDir, "src", "usedByA.ts"),
		"export function add(a: number, b: number): number {\n  return b + a;\n}\n")
	out = assertVitestSummary(t, projDir, "tc: 1 file, 1 cached, 0 run", related("src/usedByB.ts")...)
	if strings.Contains(out, "a.test.ts") {
		t.Fatalf("a.test.ts is unrelated to usedByB.ts and must not run:\n%s", out)
	}
	assertVitestSummary(t, projDir, "tc: 1 file, 0 cached, 1 run", related("src/usedByA.ts")...)

	// A rerun trigger selects every test; no source and unrelated sources select none.
	assertVitestSummary(t, projDir, "tc: 2 files, 2 cached, 0 run", related("package.json")...)
	assertVitestSummary(t, projDir, "tc: 0 files, 0 cached, 0 run", related()...)
	assertVitestSummary(t, projDir, "tc: 0 files, 0 cached, 0 run", related("src/missing.css")...)
}
