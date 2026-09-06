package main

import (
	"context"
	"strings"
	"testing"

	"github.com/steveyegge/beads/codemapops"
	"github.com/steveyegge/beads/internal/codemap/cache"
)

func TestPreToolContextLines(t *testing.T) {
	f := cache.File{Files: map[string]cache.Entry{"cmd/bd/show.go": {Path: "cmd/bd/show.go", Summary: "Renders one issue", Layer: "cli", Stale: true,
		Imports: []string{"internal/ui"}, Importers: []string{"cmd/bd/main.go"}, OpenIssues: []codemapops.IssueRef{{ID: "bd-1", Title: "x", Status: "in_progress"}}}}}
	got := preToolContext(f, "cmd/bd/show.go")
	for _, want := range []string{"cmd/bd/show.go", "Renders one issue", "stale", "imported by cmd/bd/main.go", "bd-1"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %q", want, got)
		}
	}
	if strings.Count(got, "\n") > 4 {
		t.Errorf("more than 4 lines: %q", got)
	}
	if preToolContext(f, "unknown.go") != "" {
		t.Error("unknown path must be silent")
	}
}

// TestPreToolContextSparseEntry pins that an entry the map knows nothing about
// beyond its path still injects nothing but a header and the follow-up — a hook
// that prints "imports:" with an empty list wastes a line of every prompt.
func TestPreToolContextSparseEntry(t *testing.T) {
	f := cache.File{Files: map[string]cache.Entry{"a/a.go": {Path: "a/a.go"}}}
	got := preToolContext(f, "a/a.go")
	if got == "" {
		t.Fatal("a known path must still announce itself")
	}
	if strings.Contains(got, "imports") || strings.Contains(got, "open issues") {
		t.Errorf("empty lists must be omitted: %q", got)
	}
	if n := strings.Count(got, "\n"); n > 1 {
		t.Errorf("sparse entry should be two lines, got %d newlines: %q", n, got)
	}
	if preToolContext(cache.File{}, "a/a.go") != "" {
		t.Error("an empty cache must be silent")
	}
}

func TestPostToolTracksOnlyEditingTools(t *testing.T) {
	for _, name := range []string{"Edit", "Write", "MultiEdit", "NotebookEdit"} {
		if !postToolTracksTool(name) {
			t.Errorf("%s must be tracked", name)
		}
	}
	for _, name := range []string{"Read", "Bash", "Grep", "", "edit"} {
		if postToolTracksTool(name) {
			t.Errorf("%s must not be tracked", name)
		}
	}
}

// TestPreToolSkipsStoreInitButPostToolDoesNot pins the reason codemap-hook is a
// parent with two subcommands rather than one command taking an argument:
// commandOptsOutOfStore walks the ancestor chain, so annotating the leaf
// exempts pre-tool from store initialization without exempting its sibling.
// A pre-tool that opened the embedded store would cost seconds and contend
// with whatever else the agent is running.
func TestPreToolSkipsStoreInitButPostToolDoesNot(t *testing.T) {
	if !commandOptsOutOfStore(codemapHookPreToolCmd) {
		t.Error("pre-tool must skip store initialization")
	}
	if commandOptsOutOfStore(codemapHookPostToolCmd) {
		t.Error("post-tool is a write path and must open the store")
	}
	if !codemapHookCmd.Hidden || !codemapHookPreToolCmd.Hidden {
		t.Error("codemap-hook is plumbing and must stay hidden")
	}
}

// TestCodemapHooksHonorTheOptOut pins that BD_NO_CODEMAP means the same thing
// for the tool hooks as it does for the post-commit git hook. An opt-out that
// covers one of three hooks is worse than none.
func TestCodemapHooksHonorTheOptOut(t *testing.T) {
	t.Setenv("BD_NO_CODEMAP", "1")

	var out strings.Builder
	in := `{"session_id":"s","cwd":"/nope","tool_name":"Read","tool_input":{"file_path":"/nope/a.go"}}`
	if err := runCodemapPreToolHook(strings.NewReader(in), &out); err != nil {
		t.Fatalf("pre-tool: %v", err)
	}
	if got := strings.TrimSpace(out.String()); got != "{}" {
		t.Errorf("pre-tool under BD_NO_CODEMAP = %q, want {}", got)
	}
	// The disabled post-tool must not even decode, let alone reach the store.
	if err := recordPostToolEdit(context.Background(), strings.NewReader(in)); err != nil {
		t.Errorf("post-tool under BD_NO_CODEMAP: %v", err)
	}
}
