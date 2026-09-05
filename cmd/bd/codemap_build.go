package main

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/steveyegge/beads/codemapops"
	"github.com/steveyegge/beads/internal/codemap/cache"
	"github.com/steveyegge/beads/internal/codemap/scout"
)

// summariesNotYet is the exact notice `--summaries` prints until the
// summarizer pass exists. Accepting the flag and saying so beats rejecting it:
// the flag is in the shipped help for `bd codemap refresh` already.
const summariesNotYet = "Warning: --summaries is not available yet (bd codemap refresh --summaries lands with the summarizer)"

func init() {
	codemapBuildCmd.Flags().Bool("summaries", false, "Also summarize changed files (not available yet)")
	codemapRefreshCmd.Flags().Bool("summaries", false, "Also summarize changed files (not available yet)")
	codemapCmd.AddCommand(codemapBuildCmd, codemapRefreshCmd, codemapStatusCmd)
}

var codemapBuildCmd = &cobra.Command{
	Use:           "build",
	Short:         "Scout the repository and (re)write the whole code map",
	Args:          cobra.NoArgs,
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, _ []string) error {
		if summaries, _ := cmd.Flags().GetBool("summaries"); summaries {
			fmt.Fprintln(os.Stderr, summariesNotYet)
		}
		root, repoID, err := codemapRepoRoot()
		if err != nil {
			return HandleErrorRespectJSON("%v", err)
		}
		head, err := codemapHeadSHA(root)
		if err != nil {
			return HandleErrorRespectJSON("%v", err)
		}
		graphs, missing, err := scout.ScanAll(root, nil)
		if err != nil {
			return HandleErrorRespectJSON("scout: %v", err)
		}
		for _, m := range missing {
			fmt.Fprintf(os.Stderr, "Warning: %v (that language was skipped)\n", m)
		}
		if len(graphs) == 0 {
			return HandleErrorRespectJSON("no supported language detected (go.mod or Cargo.toml)")
		}
		// ONE apply over ONE merged graph. Applying per language with
		// Only == nil would make each language's whole-repo apply delete the
		// previous language's nodes; Lang already lives on every node, so the
		// merged graph loses nothing.
		merged := mergeGraphs(graphs)
		indexer, err := openCodeMapIndexer()
		if err != nil {
			return HandleErrorRespectJSON("%v", err)
		}
		ctx := rootCtx
		res, err := indexer.Apply(ctx, codemapops.ApplyRequest{RepoID: repoID, HeadSHA: head, Graph: merged})
		if err != nil {
			return HandleErrorRespectJSON("apply: %v", err)
		}
		commandDidWrite.Store(true)

		paths := filePathsOf(merged)
		if err := rebuildCache(ctx, repoID, head, paths); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: cache: %v\n", err)
		}
		langs := langsOf(graphs)
		if jsonOutput {
			return outputJSON(map[string]any{
				"repo_id": repoID, "head_sha": head,
				"nodes_upserted": res.NodesUpserted, "nodes_deleted": res.NodesDeleted,
				"edges_written": res.EdgesWritten, "edges_pruned": res.EdgesPruned,
				"dropped": merged.Dropped, "languages": langs,
			})
		}
		renderBuilt(os.Stdout, res, langs, merged.Dropped)
		return nil
	},
}

var codemapRefreshCmd = &cobra.Command{
	Use:           "refresh",
	Short:         "Rescan only the packages whose files changed since the last build",
	Args:          cobra.NoArgs,
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, _ []string) error {
		if summaries, _ := cmd.Flags().GetBool("summaries"); summaries {
			fmt.Fprintln(os.Stderr, summariesNotYet)
		}
		root, repoID, err := codemapRepoRoot()
		if err != nil {
			return HandleErrorRespectJSON("%v", err)
		}
		res, err := refreshCodemap(rootCtx, root, repoID)
		if err != nil {
			return HandleErrorRespectJSON("%v", err)
		}
		if jsonOutput {
			return outputJSON(map[string]any{
				"repo_id": repoID, "head_sha": res.HeadSHA, "only": res.Only,
				"nodes_upserted": res.NodesUpserted, "nodes_deleted": res.NodesDeleted,
				"edges_written": res.EdgesWritten, "edges_pruned": res.EdgesPruned,
			})
		}
		renderRefreshed(os.Stdout, res)
		return nil
	},
}

var codemapStatusCmd = &cobra.Command{
	Use:           "status",
	Short:         "Report how fresh the code map is",
	Args:          cobra.NoArgs,
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(_ *cobra.Command, _ []string) error {
		root, repoID, err := codemapRepoRoot()
		if err != nil {
			return HandleErrorRespectJSON("%v", err)
		}
		head, err := codemapHeadSHA(root)
		if err != nil {
			return HandleErrorRespectJSON("%v", err)
		}
		reader, err := openCodeMapReader()
		if err != nil {
			return HandleErrorRespectJSON("%v", err)
		}
		shape, err := reader.Shape(rootCtx, repoID, codemapops.ShapeOptions{})
		if err != nil {
			return HandleErrorRespectJSON("%v", notIndexedHint(err))
		}
		st := codemapStatus{Shape: shape, HeadSHA: head, Languages: detectedLanguages(root)}
		st.UpToDate = shape.HeadSHA == head
		if !st.UpToDate {
			// A count we cannot get (the last indexed commit is not an
			// ancestor of HEAD, say, after a rebase) stays absent rather than
			// being reported as zero commits behind.
			if out, err := runGitAt(root, "rev-list", "--count", shape.HeadSHA+".."+head); err == nil {
				if n, convErr := strconv.Atoi(out); convErr == nil {
					st.Behind, st.BehindKnown = n, true
				}
			}
		}
		if beadsDir, dirErr := codemapBeadsDir(); dirErr == nil {
			if f, cacheErr := cache.Read(beadsDir); cacheErr == nil {
				st.CacheAge, st.CacheKnown = time.Since(f.GeneratedAt), true
			}
		}
		if jsonOutput {
			payload := map[string]any{
				"repo_id": repoID, "head_sha": head, "last_sha": shape.HeadSHA,
				"up_to_date": st.UpToDate, "nodes": shape.Nodes, "edges": shape.Edges,
				"stale_summaries": shape.StaleSummaries, "languages": st.Languages,
				"indexed_at": shape.IndexedAt,
			}
			if st.BehindKnown {
				payload["behind_commits"] = st.Behind
			}
			if st.CacheKnown {
				payload["cache_age_seconds"] = int(st.CacheAge.Seconds())
			}
			return outputJSON(payload)
		}
		renderCodemapStatus(os.Stdout, st)
		return nil
	},
}

// codemapStatus is what `bd codemap status` reports: the indexed shape plus
// how far it has drifted from the working tree.
type codemapStatus struct {
	Shape       codemapops.Shape
	HeadSHA     string
	UpToDate    bool
	Behind      int
	BehindKnown bool
	CacheAge    time.Duration
	CacheKnown  bool
	Languages   []string
}

// refreshResult reports what one incremental rescan changed, and which
// packages it was scoped to.
type refreshResult struct {
	Only                                                   []string
	NodesUpserted, NodesDeleted, EdgesWritten, EdgesPruned int
	HeadSHA                                                string
}

// refreshCodemap rescans only the packages owning the files that changed since
// the last indexed commit. It lives here rather than inside the cobra RunE
// because `bd codemap record-commit` calls exactly this.
func refreshCodemap(ctx context.Context, root, repoID string) (refreshResult, error) {
	var out refreshResult
	head, err := codemapHeadSHA(root)
	if err != nil {
		return out, err
	}
	out.HeadSHA = head
	reader, err := openCodeMapReader()
	if err != nil {
		return out, err
	}
	shape, err := reader.Shape(ctx, repoID, codemapops.ShapeOptions{})
	if err != nil {
		return out, notIndexedHint(err)
	}
	last := shape.HeadSHA
	if last == head {
		return out, nil
	}
	changed, err := gitChangedFiles(root, last, head)
	if err != nil {
		return out, err
	}
	if len(changed) == 0 {
		return out, nil
	}
	graphs, missing, err := scout.ScanAll(root, scout.PackageDirsFor(changed))
	if err != nil {
		return out, fmt.Errorf("scout: %w", err)
	}
	for _, m := range missing {
		fmt.Fprintf(os.Stderr, "Warning: %v (that language was skipped)\n", m)
	}
	indexer, err := openCodeMapIndexer()
	if err != nil {
		return out, err
	}
	// One apply per language, each scoped by Only to the packages that
	// language actually rescanned, so no language deletes another's nodes and
	// an untouched package keeps its rows.
	var rescanned []string
	for _, g := range graphs {
		only := packagePathsOf(g)
		if len(only) == 0 {
			continue
		}
		res, applyErr := indexer.Apply(ctx, codemapops.ApplyRequest{RepoID: repoID, HeadSHA: head, Graph: g, Only: only})
		if applyErr != nil {
			return out, fmt.Errorf("apply %s: %w", g.Lang, applyErr)
		}
		out.Only = append(out.Only, only...)
		out.NodesUpserted += res.NodesUpserted
		out.NodesDeleted += res.NodesDeleted
		out.EdgesWritten += res.EdgesWritten
		out.EdgesPruned += res.EdgesPruned
		rescanned = append(rescanned, filePathsOf(g)...)
	}
	sort.Strings(out.Only)
	commandDidWrite.Store(true)

	deleted, err := gitDeletedFiles(root, last, head)
	if err != nil {
		return out, err
	}
	if err := rebuildCache(ctx, repoID, head, mergeCachePaths(rescanned, deleted)); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: cache: %v\n", err)
	}
	return out, nil
}

// rebuildCache reprojects the derived cache for paths. The cache is
// disposable, so a missing .beads directory is an error the caller may warn
// about rather than a failed refresh.
func rebuildCache(ctx context.Context, repoID, head string, paths []string) error {
	beadsDir, err := codemapBeadsDir()
	if err != nil {
		return err
	}
	reader, err := openCodeMapReader()
	if err != nil {
		return err
	}
	files, err := openIssueFiles()
	if err != nil {
		return err
	}
	f, err := cache.Build(ctx, repoID, head, reader, files, paths)
	if err != nil {
		return err
	}
	return cache.Write(beadsDir, f)
}

// mergeCachePaths folds the freshly rescanned paths into whatever the previous
// cache covered, dropping the files this range deleted. Without the merge an
// incremental refresh would shrink the cache to just the changed packages.
func mergeCachePaths(rescanned, deleted []string) []string {
	gone := make(map[string]struct{}, len(deleted))
	for _, d := range deleted {
		gone[d] = struct{}{}
	}
	seen := make(map[string]struct{})
	var out []string
	add := func(p string) {
		if _, dead := gone[p]; dead {
			return
		}
		if _, dup := seen[p]; dup {
			return
		}
		seen[p] = struct{}{}
		out = append(out, p)
	}
	if beadsDir, err := codemapBeadsDir(); err == nil {
		if prev, err := cache.Read(beadsDir); err == nil {
			prevPaths := make([]string, 0, len(prev.Files))
			for p := range prev.Files {
				prevPaths = append(prevPaths, p)
			}
			sort.Strings(prevPaths)
			for _, p := range prevPaths {
				add(p)
			}
		}
	}
	for _, p := range rescanned {
		add(p)
	}
	return out
}

func gitChangedFiles(root, from, to string) ([]string, error) {
	out, err := runGitAt(root, "diff", "--name-only", from+".."+to)
	if err != nil {
		return nil, err
	}
	return splitGitLines(out), nil
}

func gitDeletedFiles(root, from, to string) ([]string, error) {
	out, err := runGitAt(root, "diff", "--name-only", "--diff-filter=D", from+".."+to)
	if err != nil {
		return nil, err
	}
	return splitGitLines(out), nil
}

func splitGitLines(s string) []string {
	var out []string
	for _, line := range strings.Split(s, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			out = append(out, line)
		}
	}
	return out
}

// mergeGraphs concatenates every scanned language into the single graph a
// whole-repository apply replaces the map with.
func mergeGraphs(graphs []codemapops.Graph) codemapops.Graph {
	if len(graphs) == 1 {
		return graphs[0]
	}
	var merged codemapops.Graph
	langs := make([]string, 0, len(graphs))
	external := map[string]struct{}{}
	for _, g := range graphs {
		langs = append(langs, g.Lang)
		merged.Nodes = append(merged.Nodes, g.Nodes...)
		merged.Edges = append(merged.Edges, g.Edges...)
		merged.Dropped += g.Dropped
		for _, e := range g.ExternalPackages {
			external[e] = struct{}{}
		}
	}
	for e := range external {
		merged.ExternalPackages = append(merged.ExternalPackages, e)
	}
	sort.Strings(merged.ExternalPackages)
	merged.Lang = strings.Join(langs, "+")
	return merged
}

func packagePathsOf(g codemapops.Graph) []string {
	var out []string
	for _, n := range g.Nodes {
		if n.Kind == codemapops.NodePackage {
			out = append(out, n.Path)
		}
	}
	return out
}

func filePathsOf(g codemapops.Graph) []string {
	var out []string
	for _, n := range g.Nodes {
		if n.Kind == codemapops.NodeFile {
			out = append(out, n.Path)
		}
	}
	return out
}

func langsOf(graphs []codemapops.Graph) []string {
	out := make([]string, 0, len(graphs))
	for _, g := range graphs {
		out = append(out, g.Lang)
	}
	return out
}

// detectedLanguages names the scouts that recognize root, which is what
// `status` can honestly report: the language mix is not stored on the map.
func detectedLanguages(root string) []string {
	scouts := scout.Detect(root)
	out := make([]string, 0, len(scouts))
	for _, s := range scouts {
		out = append(out, s.Name())
	}
	return out
}
