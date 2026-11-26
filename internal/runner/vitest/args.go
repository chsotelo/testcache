package vitest

import "strings"

type tcRunnerFlag string

const (
	tcFlagNoCache      tcRunnerFlag = "--no-cache"
	tcFlagNoRevalidate tcRunnerFlag = "--no-revalidate"
	subcommandRun      tcRunnerFlag = "run"
	subcommandRelated  tcRunnerFlag = "related"
	flagRun            tcRunnerFlag = "--run"
	// argsTerminator ends the options: Vitest ignores everything after a bare "--".
	argsTerminator tcRunnerFlag = "--"
)

// ParsedArgs captures parsed Vitest CLI options and positional filters.
type ParsedArgs struct {
	ConfigPath    string
	RootDir       string
	Mode          string
	HasReporter   bool
	HasMaxWorkers bool
	BypassCache   bool
	NoCache       bool
	NoRevalidate  bool
	Positionals   []string
	// Related is `vitest related <sources> --run`: the tests to run are the ones that import a
	// source, and the positionals are those sources instead of test filters.
	Related        bool
	RelatedSources []string
	ForwardFlags   []string
	CacheableFlags []string
	DiscoveryFlags []string
}

var valueFlags = map[string]bool{
	"-r": true, "--root": true,
	"-c": true, "--config": true,
	"-t": true, "--testNamePattern": true,
	"--dir": true, "--reporter": true,
	"--outputFile": true, "--mode": true,
	"--pool": true, "--execArgv": true,
	"--vmMemoryLimit": true, "--maxWorkers": true, "--max-workers": true,
	"--environment": true, "--testTimeout": true, "--hookTimeout": true,
	"--bail": true, "--retry": true, "--diff": true,
	"--exclude": true, "--project": true,
	"--slowTestThreshold": true, "--teardownTimeout": true,
	"--maxConcurrency": true, "--attachmentsDir": true,
	"--configLoader": true, "--tagsFilter": true,
	"--shard": true, "--sequence": true,
	// Vitest 5.
	"-p": true, "--repeats": true, "--fsModuleCachePath": true,
	"--browser": true, "--experimental": true,
}

var bypassFlags = map[string]bool{
	"--watch": true, "-w": true,
	"--ui": true, "--help": true, "-h": true,
	"--version": true, "-v": true,
	"--clearCache": true, "--listTags": true,
	"--mergeReports": true, "--update": true, "-u": true,
	"--coverage": true, "--typecheck": true,
	"--changed": true, "--standalone": true,
	"--no-isolate": true,
	"--browser":    true,
	// Vitest 5: Vitest picks the files from its own record of earlier runs, and tc would
	// replay cached passes for files the user did not ask for.
	"--lastFailed": true, "--sinceLastRun": true,
	// A module served from Vitest's on-disk transform cache is invisible to tc.
	"--fsModuleCache": true,
	// Vitest splits the files it is given: each shard would replay every cached file and
	// split a different set of misses, so a file could run in no shard at all.
	"--shard": true,
}

var bypassSubcommands = map[string]bool{
	"watch": true, "bench": true, "list": true,
	"typecheck": true, "init": true,
}

var nonCacheableKeyFlags = map[string]bool{
	"--reporter": true, "--maxWorkers": true, "--max-workers": true,
	"--no-color": true, "--color": true, "--clearScreen": true,
	"--silent": true, "--hideSkippedTests": true,
	// Only the exit code of a run that selects no file depends on it, never a file's result.
	"--passWithNoTests": true,
}

var discoveryRelevantFlags = map[string]bool{
	"--dir": true, "--exclude": true, "--project": true, "-p": true,
	"--mode": true, "--configLoader": true, "--tagsFilter": true, "--strictTags": true,
}

// ParseArgs separates tc flags, Vitest config/root overrides, and positional test targets.
func ParseArgs(args []string) ParsedArgs {
	out := ParsedArgs{Mode: "test"}
	var scan argScan
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == string(argsTerminator) {
			break
		}
		if scan.consumeWord(&out, arg) {
			continue
		}
		if !strings.HasPrefix(arg, "-") {
			out.addPositional(arg)
			continue
		}
		i = parseFlag(&out, args, i)
	}
	// Without --run, `vitest related` watches on a terminal: leave it to Vitest.
	if out.Related && !scan.sawRun {
		out.BypassCache = true
	}
	return out
}

// argScan is what ParseArgs remembers between words.
type argScan struct {
	sawRun        bool
	sawSubcommand bool
}

// consumeWord handles the words that are neither Vitest flags nor filters: tc's own flags,
// --run, and a subcommand in first position. It reports whether the word was one of them.
func (s *argScan) consumeWord(out *ParsedArgs, arg string) bool {
	switch arg {
	case string(tcFlagNoCache):
		out.NoCache = true
		return true
	case string(tcFlagNoRevalidate):
		out.NoRevalidate = true
		return true
	case string(flagRun):
		s.sawRun = true
		return true
	}
	if s.sawSubcommand || len(out.Positionals) > 0 || len(out.ForwardFlags) > 0 {
		return false
	}
	switch {
	case arg == string(subcommandRun):
	case arg == string(subcommandRelated):
		out.Related = true
	case bypassSubcommands[arg]:
		out.BypassCache = true
	default:
		return false
	}
	s.sawSubcommand = true
	return true
}

// addPositional records a test filter, or a source file for `vitest related`.
func (p *ParsedArgs) addPositional(arg string) {
	if p.Related {
		p.RelatedSources = append(p.RelatedSources, arg)
		return
	}
	p.Positionals = append(p.Positionals, arg)
}

func parseFlag(out *ParsedArgs, args []string, i int) int {
	arg := args[i]
	name, val, hasEq := strings.Cut(arg, "=")
	if bypassFlags[name] {
		out.BypassCache = true
	}
	if name == "--reporter" {
		out.HasReporter = true
	}
	if name == "--maxWorkers" || name == "--max-workers" {
		out.HasMaxWorkers = true
	}
	consumedNext := false
	if !hasEq && valueFlags[name] && i+1 < len(args) {
		val = args[i+1]
		i++
		consumedNext = true
	}
	if name == "--isolate" && val == "false" {
		out.BypassCache = true
	}
	switch name {
	case "-c", "--config":
		out.ConfigPath = val
		return i
	case "-r", "--root":
		out.RootDir = val
		return i
	case "--mode":
		if val != "" {
			out.Mode = val
		}
	}
	out.ForwardFlags = append(out.ForwardFlags, arg)
	if consumedNext {
		out.ForwardFlags = append(out.ForwardFlags, val)
	}
	appendFlagLists(out, flagRecord{name: name, arg: arg, val: val, consumedNext: consumedNext})
	return i
}

type flagRecord struct {
	name, arg, val string
	consumedNext   bool
}

func appendFlagLists(out *ParsedArgs, f flagRecord) {
	if discoveryRelevantFlags[f.name] {
		out.DiscoveryFlags = append(out.DiscoveryFlags, f.arg)
		if f.consumedNext {
			out.DiscoveryFlags = append(out.DiscoveryFlags, f.val)
		}
	}
	if !nonCacheableKeyFlags[f.name] {
		if f.consumedNext {
			out.CacheableFlags = append(out.CacheableFlags, f.name+"="+f.val)
		} else {
			out.CacheableFlags = append(out.CacheableFlags, f.arg)
		}
	}
}
