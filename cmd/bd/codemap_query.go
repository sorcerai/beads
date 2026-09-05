package main

import (
	"context"
	"errors"
	"os"

	"github.com/spf13/cobra"

	"github.com/steveyegge/beads/beadserrors"
	"github.com/steveyegge/beads/codemapops"
)

func init() {
	codemapDepsCmd.Flags().Bool("reverse", false, "Walk importers instead of imports")
	codemapDepsCmd.Flags().Int("depth", 2, "How many hops to walk")
	codemapStaleCmd.Flags().Int("limit", 50, "Maximum paths to list")
	codemapWhoCmd.Flags().Bool("all", false, "Include closed issues")
	codemapCmd.AddCommand(codemapShowCmd, codemapDepsCmd, codemapStaleCmd, codemapFilesCmd, codemapWhoCmd)
}

var codemapShowCmd = &cobra.Command{
	Use:           "show <path|package>",
	Short:         "Show one file or package with its imports, importers and open issues",
	Args:          cobra.ExactArgs(1),
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(_ *cobra.Command, args []string) error {
		target := args[0]
		_, repoID, err := codemapRepoRoot()
		if err != nil {
			return HandleErrorRespectJSON("%v", err)
		}
		reader, err := openCodeMapReader()
		if err != nil {
			return HandleErrorRespectJSON("%v", err)
		}
		files, err := openIssueFiles()
		if err != nil {
			return HandleErrorRespectJSON("%v", err)
		}
		ctx := rootCtx

		// A file first, because a path is the common spelling; a package only
		// when the map has no file by that name.
		fc, fileErr := reader.FileContext(ctx, repoID, target)
		if fileErr != nil && !errors.Is(fileErr, beadserrors.ErrNotFound) {
			return HandleErrorRespectJSON("%v", notIndexedHint(fileErr))
		}
		var pc codemapops.PackageContext
		if fileErr != nil {
			var pkgErr error
			pc, pkgErr = reader.PackageContext(ctx, repoID, target)
			if pkgErr != nil {
				if errors.Is(pkgErr, beadserrors.ErrNotFound) {
					return HandleErrorRespectJSON("no file or package %q in the code map", target)
				}
				return HandleErrorRespectJSON("%v", notIndexedHint(pkgErr))
			}
		}
		// Issues are recorded against FILE paths, so a package lookup has no
		// issue list of its own.
		var issues []codemapops.IssueRef
		if fileErr == nil {
			if issues, err = files.ByPath(ctx, repoID, target, true); err != nil && !errors.Is(err, beadserrors.ErrNotFound) {
				return HandleErrorRespectJSON("open issues for %s: %v", target, err)
			}
		}
		if jsonOutput {
			payload := map[string]any{"issues": issues}
			if fileErr == nil {
				payload["file"] = fc
			} else {
				payload["package"] = pc
			}
			return outputJSON(payload)
		}
		if fileErr == nil {
			renderFileContext(os.Stdout, fc)
			renderIssueRefs(os.Stdout, target, issues)
		} else {
			renderPackageContext(os.Stdout, pc)
		}
		return nil
	},
}

// depRow is one node in the flattened dependency walk.
type depRow struct {
	Depth int    `json:"depth"`
	Path  string `json:"path"`
}

var codemapDepsCmd = &cobra.Command{
	Use:           "deps <path>",
	Short:         "Walk what a file imports, or what imports it",
	Args:          cobra.ExactArgs(1),
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		reverse, _ := cmd.Flags().GetBool("reverse")
		depth, _ := cmd.Flags().GetInt("depth")
		if depth < 1 {
			return HandleErrorRespectJSON("--depth must be at least 1")
		}
		_, repoID, err := codemapRepoRoot()
		if err != nil {
			return HandleErrorRespectJSON("%v", err)
		}
		reader, err := openCodeMapReader()
		if err != nil {
			return HandleErrorRespectJSON("%v", err)
		}
		rows, err := walkDeps(rootCtx, reader, repoID, args[0], reverse, depth)
		if err != nil {
			return HandleErrorRespectJSON("%v", notIndexedHint(err))
		}
		if jsonOutput {
			return outputJSON(map[string]any{"root": args[0], "reverse": reverse, "depth": depth, "nodes": rows})
		}
		renderDepTree(os.Stdout, args[0], reverse, rows)
		return nil
	},
}

// walkDeps is a breadth-first walk over the import graph, one Reader call per
// node. A visited set cuts cycles, so a mutually importing pair terminates.
func walkDeps(ctx context.Context, reader codemapops.Reader, repoID, root string, reverse bool, maxDepth int) ([]depRow, error) {
	type item struct {
		ref   codemapops.NodeRef
		depth int
	}
	start, err := neighbors(ctx, reader, repoID, codemapops.NodeRef{Path: root, Kind: codemapops.NodeFile}, reverse)
	if err != nil {
		return nil, err
	}
	visited := map[string]struct{}{root: {}}
	queue := make([]item, 0, len(start))
	for _, r := range start {
		queue = append(queue, item{ref: r, depth: 1})
	}
	var rows []depRow
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		if _, seen := visited[cur.ref.Path]; seen {
			continue
		}
		visited[cur.ref.Path] = struct{}{}
		rows = append(rows, depRow{Depth: cur.depth, Path: cur.ref.Path})
		if cur.depth >= maxDepth {
			continue
		}
		next, err := neighbors(ctx, reader, repoID, cur.ref, reverse)
		if err != nil {
			// A neighbor the map cannot expand (an external package, say) is
			// a leaf, not a failed walk.
			if errors.Is(err, beadserrors.ErrNotFound) {
				continue
			}
			return nil, err
		}
		for _, r := range next {
			queue = append(queue, item{ref: r, depth: cur.depth + 1})
		}
	}
	return rows, nil
}

// neighbors reads one node's imports or importers, dispatching on whether the
// node is a file or a package.
func neighbors(ctx context.Context, reader codemapops.Reader, repoID string, ref codemapops.NodeRef, reverse bool) ([]codemapops.NodeRef, error) {
	if ref.Kind == codemapops.NodePackage {
		pc, err := reader.PackageContext(ctx, repoID, ref.Path)
		if err != nil {
			return nil, err
		}
		if reverse {
			return pc.Importers, nil
		}
		return pc.Imports, nil
	}
	fc, err := reader.FileContext(ctx, repoID, ref.Path)
	if err != nil {
		return nil, err
	}
	if reverse {
		return fc.Importers, nil
	}
	return fc.Imports, nil
}

var codemapStaleCmd = &cobra.Command{
	Use:           "stale",
	Short:         "List files whose summary predates their current contents",
	Args:          cobra.NoArgs,
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, _ []string) error {
		limit, _ := cmd.Flags().GetInt("limit")
		_, repoID, err := codemapRepoRoot()
		if err != nil {
			return HandleErrorRespectJSON("%v", err)
		}
		reader, err := openCodeMapReader()
		if err != nil {
			return HandleErrorRespectJSON("%v", err)
		}
		refs, err := reader.Stale(rootCtx, repoID, limit)
		if err != nil {
			return HandleErrorRespectJSON("%v", notIndexedHint(err))
		}
		if jsonOutput {
			return outputJSON(map[string]any{"stale": refs, "count": len(refs)})
		}
		renderCodemapStale(os.Stdout, refs)
		return nil
	},
}

var codemapFilesCmd = &cobra.Command{
	Use:           "files <issue-id>",
	Short:         "List the files an issue has touched",
	Args:          cobra.ExactArgs(1),
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(_ *cobra.Command, args []string) error {
		files, err := openIssueFiles()
		if err != nil {
			return HandleErrorRespectJSON("%v", err)
		}
		id, err := resolveCodemapIssueID(rootCtx, args[0])
		if err != nil {
			return HandleErrorRespectJSON("resolving %s: %v", args[0], err)
		}
		rows, err := files.ByIssue(rootCtx, id)
		if err != nil {
			return HandleErrorRespectJSON("%v", err)
		}
		if jsonOutput {
			return outputJSON(map[string]any{"issue_id": id, "files": rows})
		}
		renderIssueFiles(os.Stdout, id, rows)
		return nil
	},
}

var codemapWhoCmd = &cobra.Command{
	Use:           "who <path>",
	Short:         "List the issues touching a path",
	Args:          cobra.ExactArgs(1),
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		all, _ := cmd.Flags().GetBool("all")
		_, repoID, err := codemapRepoRoot()
		if err != nil {
			return HandleErrorRespectJSON("%v", err)
		}
		files, err := openIssueFiles()
		if err != nil {
			return HandleErrorRespectJSON("%v", err)
		}
		refs, err := files.ByPath(rootCtx, repoID, args[0], !all)
		if err != nil {
			return HandleErrorRespectJSON("%v", err)
		}
		if jsonOutput {
			return outputJSON(map[string]any{"path": args[0], "issues": refs})
		}
		renderIssueRefs(os.Stdout, args[0], refs)
		return nil
	},
}
