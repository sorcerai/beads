package main

import (
	"fmt"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/rollup"
)

var boardCmd = &cobra.Command{
	Use:   "board",
	Short: "Project board rollup (project -> epics -> status columns)",
	Long: `Read-only Linear-style rollup. Groups issues by project:<slug> label,
nests child issues under their epic, and buckets by status category
(todo/in_progress/done/deferred). --json prints the canonical contract
consumed by the web dashboard and the MCP tool.`,
	Run: func(cmd *cobra.Command, args []string) {
		project, _ := cmd.Flags().GetString("project")
		limit, _ := cmd.Flags().GetInt("limit")
		ctx := rootCtx

		opts := buildBoardOptions(project, limit)
		r, err := rollup.Compute(ctx, store, opts)
		if err != nil {
			FatalErrorRespectJSON("computing board: %v", err)
		}
		if jsonOutput {
			outputJSON(r)
			return
		}
		renderBoardText(r)
	},
}

// buildBoardOptions is unit-testable without a store. v1 pagination is
// --limit + DefaultLimit caps (spec C2); cursor-based pagination is a
// documented future enhancement and is intentionally NOT exposed as a flag
// until it actually does something (an advertised no-op flag is a footgun).
// TODO(followup): populate rollup.Options.CustomCategories from the store so
// custom statuses map to their declared column instead of the fallback one.
func buildBoardOptions(project string, limit int) rollup.Options {
	return rollup.Options{Project: project, Limit: limit}
}

func renderBoardText(r *rollup.Rollup) {
	fmt.Printf("Board @ %s\n", r.GeneratedAt.Format("2006-01-02 15:04:05 UTC"))
	for _, p := range r.Projects {
		fmt.Printf("\n## %s  (%d epics, %d without an epic)\n", p.Slug, len(p.Epics), len(p.Loose))
		for _, e := range p.Epics {
			flag := ""
			if e.Conflict {
				flag = "  [CONFLICT: closed epic, open children]"
			}
			fmt.Printf("  [%s] %s — %s%s\n", e.Column, e.Issue.ID, e.Issue.Title, flag)
			for _, c := range e.Children {
				fmt.Printf("      [%s] %s — %s (%s)\n", c.Column, c.ID, c.Title, c.Status)
			}
		}
	}
	if len(r.Diagnostics) > 0 {
		fmt.Printf("\nDiagnostics: %d (run with --json for detail)\n", len(r.Diagnostics))
	}
}

func init() {
	boardCmd.Flags().String("project", "", "Scope to a single project slug")
	boardCmd.Flags().Int("limit", 0, "Max issues to scan (0 = default cap)")
	rootCmd.AddCommand(boardCmd)
}
