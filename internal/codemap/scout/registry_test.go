package scout

import (
	"errors"
	"testing"

	"github.com/steveyegge/beads/codemapops"
)

type stubScout struct {
	name    string
	detects bool
	err     error
}

func (s stubScout) Name() string       { return s.name }
func (s stubScout) Detect(string) bool { return s.detects }
func (s stubScout) Scan(string, []string) (codemapops.Graph, error) {
	if s.err != nil {
		return codemapops.Graph{}, s.err
	}
	return codemapops.Graph{Lang: s.name}, nil
}

// withRegistry swaps the package registry for the duration of a test.
func withRegistry(t *testing.T, scouts ...Scout) {
	t.Helper()
	registryMu.Lock()
	saved := registry
	registry = scouts
	registryMu.Unlock()
	t.Cleanup(func() {
		registryMu.Lock()
		registry = saved
		registryMu.Unlock()
	})
}

func TestScanAllSkipsMissingToolsAndPropagatesRealErrors(t *testing.T) {
	boom := errors.New("boom")
	withRegistry(t,
		stubScout{name: "ok", detects: true},
		stubScout{name: "absent", detects: true, err: &ErrToolMissing{Tool: "cargo"}},
		stubScout{name: "undetected", detects: false},
	)
	graphs, missing, err := ScanAll(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(graphs) != 1 || graphs[0].Lang != "ok" {
		t.Errorf("graphs: %+v", graphs)
	}
	if len(missing) != 1 || missing[0].Error() != "cargo is not on PATH" {
		t.Errorf("missing: %v", missing)
	}

	withRegistry(t, stubScout{name: "broken", detects: true, err: boom})
	if _, _, err := ScanAll(t.TempDir(), nil); !errors.Is(err, boom) {
		t.Fatalf("a real scan failure must abort: %v", err)
	}
}

func TestDetectReturnsOnlyMatchingScouts(t *testing.T) {
	withRegistry(t, stubScout{name: "a", detects: true}, stubScout{name: "b", detects: false})
	found := Detect(t.TempDir())
	if len(found) != 1 || found[0].Name() != "a" {
		t.Errorf("Detect: %+v", found)
	}
}
