package main

import (
	"context"

	"github.com/spf13/cobra"
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

// TestBothToolHooksSkipRootStoreInit pins the annotation both halves need, for
// two different reasons.
//
// pre-tool never wants a store at all: it reads the derived cache, and opening
// the embedded store would cost seconds and contend with whatever else the
// agent is running.
//
// post-tool DOES want one — it is a write path — but it must open it ITSELF,
// because the root pre-run opens before RunE can read stdin and an open that
// happens before the handler's context exists cannot be bounded by it. The
// budget has to cover the slowest step or it is decoration.
func TestBothToolHooksSkipRootStoreInit(t *testing.T) {
	for _, cmd := range []*cobra.Command{codemapHookPreToolCmd, codemapHookPostToolCmd} {
		if !commandOptsOutOfStore(cmd) {
			t.Errorf("%s must skip the root pre-run's store init", cmd.Name())
		}
		if !cmd.Hidden {
			t.Errorf("%s is plumbing and must stay hidden", cmd.Name())
		}
	}
	if !codemapHookCmd.Hidden {
		t.Error("codemap-hook is plumbing and must stay hidden")
	}
}

// TestPostToolBudgetIsConfigurable pins the knob the budget regression test
// uses, and that a nonsense value falls back rather than disabling the bound.
func TestPostToolBudgetIsConfigurable(t *testing.T) {
	for env, want := range map[string]string{"": "2s", "1ms": "1ms", "5": "5s", "junk": "2s"} {
		t.Setenv(codemapToolTimeoutEnv, env)
		if got := hookTimeoutFromEnv(codemapToolTimeoutEnv, postToolTimeout).String(); got != want {
			t.Errorf("%s=%q → %s, want %s", codemapToolTimeoutEnv, env, got, want)
		}
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
	if err := recordPostToolEdit(context.Background(), codemapHookPostToolCmd, strings.NewReader(in)); err != nil {
		t.Errorf("post-tool under BD_NO_CODEMAP: %v", err)
	}
}
