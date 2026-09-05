package main

import (
	"context"
	"os"

	"github.com/spf13/cobra"

	"github.com/steveyegge/beads/codemapops"
	"github.com/steveyegge/beads/internal/utils"
)

func init() {
	codemapCmd.AddCommand(codemapLinkCmd)
}

var codemapLinkCmd = &cobra.Command{
	Use:           "link <issue-id> <path>...",
	Short:         "Record by hand that an issue touches some files",
	Args:          cobra.MinimumNArgs(2),
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(_ *cobra.Command, args []string) error {
		_, repoID, err := codemapRepoRoot()
		if err != nil {
			return HandleErrorRespectJSON("%v", err)
		}
		files, err := openIssueFiles()
		if err != nil {
			return HandleErrorRespectJSON("%v", err)
		}
		id, err := resolveCodemapIssueID(rootCtx, args[0])
		if err != nil {
			return HandleErrorRespectJSON("resolving %s: %v", args[0], err)
		}
		paths := args[1:]
		// Paths are NOT validated here: RecordInTx normalizes and refuses an
		// escaping path before it probes the issue or writes a row, so the one
		// rule lives with the storage that enforces it rather than in a second
		// copy at the front door.
		res, err := files.Record(rootCtx, codemapops.RecordRequest{
			IssueID: id, RepoID: repoID, Paths: paths, Source: codemapops.SourceManual,
		})
		if err != nil {
			return HandleErrorRespectJSON("%v", err)
		}
		commandDidWrite.Store(true)
		SetLastTouchedID(id)

		if jsonOutput {
			return outputJSON(map[string]any{
				"issue_id": id, "repo_id": repoID, "paths": paths,
				"inserted": res.Inserted, "updated": res.Updated,
			})
		}
		renderLinked(os.Stdout, id, paths)
		return nil
	},
}

// resolveCodemapIssueID turns a partial id into a full one the same way every
// other verb that takes an issue argument does.
func resolveCodemapIssueID(ctx context.Context, input string) (string, error) {
	if err := ensureDirectMode("codemap"); err != nil {
		return "", err
	}
	return utils.ResolvePartialID(ctx, store, input)
}
