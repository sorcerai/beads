package main

import (
	"os"
	"strings"
	"testing"
	"time"
)

func TestDocsRegenPromptContents(t *testing.T) {
	t.Parallel()
	repo := docsTestRepo(t)
	iss := testIssueForEntry()
	writeDocsEntryForIssue(nil, repo, "wiki", iss, "", nil, nil)

	prompt := buildDocsRegenPrompt(repo, "wiki")
	for _, want := range []string{
		repo,
		"bd docs regen --complete",
		"ARCH.md",
		"Update existing pages in place",
		"Cite only real file paths",
		iss.ID, // inbox entry included
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt missing %q", want)
		}
	}
}

func TestDocsRegenComplete(t *testing.T) {
	t.Parallel()
	repo := docsTestRepo(t)
	iss := testIssueForEntry()
	writeDocsEntryForIssue(nil, repo, "wiki", iss, "", nil, nil)
	st, _ := readDocsState(docsStatePath(repo, "wiki"))
	st.Dirty = 7
	writeDocsState(docsStatePath(repo, "wiki"), st)

	// Truncated to second precision: writeDocsState persists RFC3339 (no
	// fractional seconds, by design — see docs_state.go), so comparing
	// against a full-precision "before" would spuriously fail on the
	// almost-always-nonzero nanosecond component.
	before := time.Now().UTC().Truncate(time.Second)
	if err := runDocsRegenComplete(repo, "wiki"); err != nil {
		t.Fatalf("complete: %v", err)
	}
	if _, err := os.Stat(docsEntryPath(repo, "wiki", iss.ID)); err == nil {
		t.Fatal("inbox entry not consumed")
	}
	got, _ := readDocsState(docsStatePath(repo, "wiki"))
	if got.Dirty != 0 || got.RegenWatermark.Before(before) {
		t.Fatalf("state not advanced: %+v", got)
	}
}
