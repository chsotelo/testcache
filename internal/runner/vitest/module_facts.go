package vitest

import (
	"encoding/json"
	"sync"
)

// moduleFacts lists inputs V8 coverage cannot show: modules imported only for their side
// effects, and the __mocks__ paths whose presence decides how a factory-less vi.mock resolves.
// wrapper.config.mjs extracts them from each module's source in the Vite main process.
type moduleFacts struct {
	BareImports    []string `json:"bareImports"`
	MockCandidates []string `json:"mockCandidates"`
}

type moduleFactsIndex struct {
	mu       sync.RWMutex
	byModule map[string]moduleFacts
}

func indexModuleFacts(rawJSON json.RawMessage) *moduleFactsIndex {
	if len(rawJSON) == 0 {
		return nil
	}
	var raw map[string]moduleFacts
	if err := json.Unmarshal(rawJSON, &raw); err != nil || len(raw) == 0 {
		return nil
	}
	return &moduleFactsIndex{byModule: raw}
}

// collectDeps returns the bare-import targets and __mocks__ candidates declared by the modules
// a test loaded. Bare-import targets are loaded modules themselves, so chains are covered.
func (idx *moduleFactsIndex) collectDeps(loadedModules []string) []string {
	if idx == nil {
		return nil
	}
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	var out []string
	for _, mod := range loadedModules {
		facts, ok := idx.byModule[mod]
		if !ok {
			continue
		}
		out = append(out, facts.BareImports...)
		out = append(out, facts.MockCandidates...)
	}
	return out
}
