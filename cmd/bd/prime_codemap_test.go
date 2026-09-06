package main

import (
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/codemapops"
)

func TestRenderPrimeCodeMapCapsAndInstructs(t *testing.T) {
	shape := codemapops.Shape{HeadSHA: "abc1234", Nodes: 900, Edges: 2000, StaleSummaries: 3,
		Layers: []codemapops.LayerSummary{{Layer: "cli", Packages: 4, Files: 300}, {Layer: "storage", Packages: 9, Files: 400}},
		TopFanIn: []codemapops.NodeRef{
			{Path: "internal/types/types.go", Summary: "Core issue types"},
			{Path: "internal/storage/storage.go", Summary: "Storage interface"},
		}}

	out := renderPrimeCodeMap(shape, 2*time.Hour, 20)
	for _, want := range []string{"## Code map", "abc1234", "3 summaries stale", "cli", "internal/types/types.go", "bd show <id>", "bd codemap show <path>"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in\n%s", want, out)
		}
	}
	if n := strings.Count(out, "\n"); n > 20 {
		t.Errorf("exceeds cap: %d lines", n)
	}

	tiny := renderPrimeCodeMap(shape, 0, 5)
	if strings.Count(tiny, "\n") > 5 {
		t.Errorf("cap not honoured: %q", tiny)
	}
	// Header, freshness and both instruction lines survive any cap.
	for _, want := range []string{"## Code map", "abc1234", "bd show <id>", "bd codemap show <path>"} {
		if !strings.Contains(tiny, want) {
			t.Errorf("capped render dropped %q:\n%s", want, tiny)
		}
	}

	// No stale summaries: the refresh clause goes away entirely.
	fresh := shape
	fresh.StaleSummaries = 0
	if got := renderPrimeCodeMap(fresh, time.Hour, 20); strings.Contains(got, "stale") {
		t.Errorf("stale clause survived a clean map:\n%s", got)
	}
}
