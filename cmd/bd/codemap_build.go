package main

import (
	"context"
	"errors"
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
	"github.com/steveyegge/beads/issueops"
)

func init() {
	for _, c := range []*cobra.Command{codemapBuildCmd, codemapRefreshCmd} {
		c.Flags().Bool("summaries", false, "Also summarize files whose summary is missing or stale (calls agy; BD_CODEMAP_FAKE_AGY=<file> substitutes that file's contents for the model)")
		c.Flags().Int("max-files", 0, "With --summaries, summarize at most N files")
	}
	codemapCmd.AddCommand(codemapBuildCmd, codemapRefreshCmd, codemapStatusCmd)
}

var codemapBuildCmd = &cobra.Command{
	Use:           "build",
	Short:         "Scout the repository and (re)write the whole code map",
	Args:          cobra.NoArgs,
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, _ []string) error {
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

		if err := rebuildCache(ctx, repoID, head, merged, true, nil); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: cache: %v\n", err)
		}
		langs := langsOf(graphs)
		if jsonOutput {
			if err := outputJSON(map[string]any{
				"repo_id": repoID, "head_sha": head,
				"nodes_upserted": res.NodesUpserted, "nodes_deleted": res.NodesDeleted,
				"edges_written": res.EdgesWritten, "edges_pruned": res.EdgesPruned,
				"dropped": merged.Dropped, "languages": langs,
			}); err != nil {
				return err
			}
		} else {
			renderBuilt(os.Stdout, res, langs, merged.Dropped)
		}
		// LAST, and after the cache rebuild for two separate reasons: the
		// rebuild recomputes staleness from the database and would undo the
		// entries the summarizer just wrote, and the map is already indexed by
		// the time a model is asked anything — a dead agy must not hide that.
		if err := maybeSummarize(ctx, cmd, root, repoID); err != nil {
			return HandleError("%v", err)
		}
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
		root, repoID, err := codemapRepoRoot()
		if err != nil {
			return HandleErrorRespectJSON("%v", err)
		}
		res, err := refreshCodemap(rootCtx, root, repoID)
		if err != nil {
			return HandleErrorRespectJSON("%v", err)
		}
		if jsonOutput {
			if err := outputJSON(map[string]any{
				"repo_id": repoID, "head_sha": res.HeadSHA, "only": res.Only,
				"nodes_upserted": res.NodesUpserted, "nodes_deleted": res.NodesDeleted,
				"edges_written": res.EdgesWritten, "edges_pruned": res.EdgesPruned,
			}); err != nil {
				return err
			}
		} else {
			renderRefreshed(os.Stdout, res)
		}
		if err := maybeSummarize(rootCtx, cmd, root, repoID); err != nil {
			return HandleError("%v", err)
		}
		return nil
	},
}

// maybeSummarize runs the summarizer pass when --summaries was asked for, and
// is a no-op otherwise: NOTHING in bd calls a model without that flag.
func maybeSummarize(ctx context.Context, cmd *cobra.Command, root, repoID string) error {
	if summaries, _ := cmd.Flags().GetBool("summaries"); !summaries {
		return nil
	}
	maxFiles, _ := cmd.Flags().GetInt("max-files")
	return runSummaries(ctx, root, repoID, maxFiles)
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
	indexer, err := openCodeMapIndexer()
	if err != nil {
		return out, err
	}
	var graphs []codemapops.Graph
	if len(changed) > 0 {
		var missing []error
		graphs, missing, err = scout.ScanAll(root, scout.PackageDirsFor(changed))
		if err != nil {
			return out, fmt.Errorf("scout: %w", err)
		}
		for _, m := range missing {
			fmt.Fprintf(os.Stderr, "Warning: %v (that language was skipped)\n", m)
		}
	}
	// One apply per language, each scoped by Only to the packages that
	// language actually rescanned, so no language deletes another's nodes and
	// an untouched package keeps its rows.
	for _, g := range graphs {
		// The union, not just the packages the scan RETURNED: a deleted
		// directory produces no node, so scoping the apply to what came back
		// would leave the vanished package's rows behind forever.
		only := unionStrings(g.ScopedPackages, packagePathsOf(g))
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
	}
	sort.Strings(out.Only)
	commandDidWrite.Store(true)

	// Nothing was in scope — an empty commit, or one whose files no scan owns.
	// The head still has to be recorded, or last_sha stays where it is and
	// `status` reports "behind HEAD" forever while every later refresh re-runs
	// the same diff from the same stale base. An empty Only records exactly
	// that and touches no row; there is also nothing for the cache to reproject.
	if len(out.Only) == 0 {
		if _, err := indexer.Apply(ctx, codemapops.ApplyRequest{RepoID: repoID, HeadSHA: head, Only: []string{}}); err != nil {
			return out, fmt.Errorf("recording head: %w", err)
		}
		return out, nil
	}

	deleted, err := gitDeletedFiles(root, last, head)
	if err != nil {
		return out, err
	}
	if err := rebuildCache(ctx, repoID, head, mergeGraphs(graphs), false, deleted); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: cache: %v\n", err)
	}
	return out, nil
}

// rebuildCache reprojects the derived cache from the graph the caller just
// applied. The cache is disposable, so a missing .beads directory is an error
// the caller may warn about rather than a failed build.
//
// full says the graph covers the whole repository, which is what decides
// whether an entry in the OLD cache that the graph does not mention is a file
// outside this scan's scope (keep it) or a file that has left the tree (drop
// it).
func rebuildCache(ctx context.Context, repoID, head string, g codemapops.Graph, full bool, deleted []string) error {
	beadsDir, err := codemapBeadsDir()
	if err != nil {
		return err
	}
	prev, err := cache.Read(beadsDir)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		// A corrupt cache is not a reason to fail the build; it is a reason to
		// rebuild from nothing.
		fmt.Fprintf(os.Stderr, "Warning: discarding unreadable code map cache: %v\n", err)
		prev = cache.File{}
	}
	prev = prunePrev(prev, g, full, deleted)

	reader, err := openCodeMapReader()
	if err != nil {
		return err
	}
	// ONE stale query for the whole repository rather than a per-file freshness
	// check. Reader.Stale defaults a limit of 0 to 100, so ask for a ceiling no
	// real repository reaches.
	stale, err := reader.Stale(ctx, repoID, staleScanLimit)
	if err != nil {
		return err
	}
	links, err := openIssueLinks(ctx, repoID)
	if err != nil {
		return err
	}
	return cache.Write(beadsDir, cache.BuildFromGraph(ctx, repoID, head, g, prev, stale, links, deleted))
}

// staleScanLimit is the "no limit" the Reader has no spelling for.
const staleScanLimit = 100000

// prunePrev drops the previous cache entries this rebuild must not carry
// forward: everything outside a whole-repository graph, and the files a
// refresh's commit range deleted.
func prunePrev(prev cache.File, g codemapops.Graph, full bool, deleted []string) cache.File {
	if prev.Files == nil {
		return prev
	}
	if full {
		keep := make(map[string]struct{}, len(g.Nodes))
		for _, n := range g.Nodes {
			if n.Kind == codemapops.NodeFile {
				keep[n.Path] = struct{}{}
			}
		}
		for p := range prev.Files {
			if _, ok := keep[p]; !ok {
				delete(prev.Files, p)
			}
		}
		return prev
	}
	for _, d := range deleted {
		delete(prev.Files, d)
	}
	return prev
}

// openIssueLinks maps each path to the open issues touching it, in
// O(open issues + touched paths) round trips rather than the O(files) ByPath
// calls the per-file cache build used to make.
//
// TWO steps because neither role method alone is both cheap and correct:
// ByIssue enumerates rows without a repository filter, so it can only propose
// CANDIDATE paths; ByPath is repo-scoped and authoritative but would cost a
// call per file if asked about all of them. Asking it only about the paths some
// open issue actually touched is a set far smaller than the tree.
func openIssueLinks(ctx context.Context, repoID string) (map[string][]codemapops.IssueRef, error) {
	reader, err := openIssueReader()
	if err != nil {
		return nil, err
	}
	page, err := reader.List(ctx, issueops.ListRequest{Status: "open", SkipLabels: true, SkipCounts: true})
	if err != nil {
		return nil, err
	}
	files, err := openIssueFiles()
	if err != nil {
		return nil, err
	}
	candidates := map[string]struct{}{}
	for _, item := range page.Items {
		if item == nil || item.Issue == nil {
			continue
		}
		rows, err := files.ByIssue(ctx, item.ID)
		if err != nil {
			return nil, err
		}
		for _, r := range rows {
			candidates[r.Path] = struct{}{}
		}
	}
	links := make(map[string][]codemapops.IssueRef, len(candidates))
	for p := range candidates {
		refs, err := files.ByPath(ctx, repoID, p, true)
		if err != nil {
			return nil, err
		}
		if len(refs) > 0 {
			links[p] = refs
		}
	}
	return links, nil
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
