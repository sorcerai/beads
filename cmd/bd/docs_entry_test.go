package main

import (
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

func testIssueForEntry() *types.Issue {
	closed := time.Date(2026, 7, 4, 6, 30, 0, 0, time.UTC)
	return &types.Issue{
		ID:          "bx-12",
		Title:       "Add widget cache",
		Description: "Cache widgets to cut API calls.",
		Priority:    2,
		IssueType:   types.TypeTask,
		CreatedAt:   time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC),
		ClosedAt:    &closed,
		CloseReason: "done: LRU cache added",
	}
}

func TestRenderDocsEntryDeterministic(t *testing.T) {
	t.Parallel()
	iss := testIssueForEntry()
	// Deliberately unsorted inputs — renderer must sort.
	a := renderDocsEntry(iss, "bx-1", []string{"bx-9", "bx-3"}, []string{"z.go", "a.go"})
	b := renderDocsEntry(iss, "bx-1", []string{"bx-3", "bx-9"}, []string{"a.go", "z.go"})
	if string(a) != string(b) {
		t.Fatal("entry bytes differ for same logical inputs (must sort + be deterministic)")
	}
	s := string(a)
	for _, want := range []string{
		"# bx-12: Add widget cache",
		"type: task",
		"priority: P2",
		"epic: bx-1",
		"closed: 2026-07-04T06:30:00Z",
		"close_reason: done: LRU cache added",
		"deps: bx-3, bx-9",
		"Cache widgets to cut API calls.",
		"- a.go",
		"- z.go",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("entry missing %q\n---\n%s", want, s)
		}
	}
	if strings.Index(s, "- a.go") > strings.Index(s, "- z.go") {
		t.Error("files not sorted")
	}
}

func TestRenderDocsEntryDegraded(t *testing.T) {
	t.Parallel()
	iss := testIssueForEntry()
	s := string(renderDocsEntry(iss, "", nil, nil))
	if !strings.Contains(s, "epic: -") {
		t.Error("missing epic placeholder")
	}
	if !strings.Contains(s, "no associated files found") {
		t.Error("empty file list must be noted inline (weak bd explain association)")
	}
}

func TestDocsEntryPathSanitizes(t *testing.T) {
	t.Parallel()
	p := docsEntryPath("/repo", "wiki", "evil/../id")
	if strings.Contains(p, "..") {
		t.Fatalf("path traversal not sanitized: %s", p)
	}
}
