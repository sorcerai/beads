package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/steveyegge/beads/codemapops"
	"github.com/steveyegge/beads/internal/agyclient"
	"github.com/steveyegge/beads/internal/codemap/cache"
	"github.com/steveyegge/beads/internal/codemap/scout"
	"github.com/steveyegge/beads/internal/codemap/summarize"
	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/ui"
)

// fakeAgyEnv points at a file whose contents stand in for the model's answer,
// so the end-to-end path is testable without a model. It is the ONLY other way
// a Caller is produced; nothing else in bd calls a model implicitly.
const fakeAgyEnv = "BD_CODEMAP_FAKE_AGY"

// summariesModelKey names the model recorded on each summary row. agy's own
// --model flag is broken in print mode, so this is a label bd writes down (and
// prints), not a switch it sends.
const summariesModelKey = "codemap.summaries.model"

// maxModelLabel matches the summary_model column.
const maxModelLabel = 64

// layersKeyFor names the per-repository layer vocabulary. It is written on
// first use and read forever after: a renamed directory must not silently
// change what an existing summary was allowed to be labeled.
func layersKeyFor(repoID string) string { return "codemap." + repoID + ".layers" }

// runSummaries summarizes the files whose summaries are missing or stale, in
// one pass: gather candidates, ask the model in batches, write what the store
// will accept, and patch the derived cache for exactly the paths that landed.
func runSummaries(ctx context.Context, root, repoID string, maxFiles int) error {
	w := summariesWriter()
	reader, err := openCodeMapReader()
	if err != nil {
		return err
	}
	stale, err := reader.Stale(ctx, repoID, staleScanLimit)
	if err != nil {
		return notIndexedHint(err)
	}
	beadsDir, err := codemapBeadsDir()
	if err != nil {
		return err
	}
	// A cache we cannot read is NOT zero never-summarized files: it is a
	// smaller candidate set than the truth, and saying so beats reporting
	// "nothing to do" on an unreadable file.
	cached, cacheErr := cache.Read(beadsDir)
	if cacheErr != nil {
		fmt.Fprintf(os.Stderr, "Warning: code map cache unreadable (%v); summarizing only the %d stale files\n", cacheErr, len(stale))
	}

	cands, err := buildCandidates(root, candidatePaths(stale, cached), cached, maxFiles)
	if err != nil {
		return err
	}
	if len(cands) == 0 {
		fmt.Fprintln(w, "Nothing to summarize: every indexed file has a current summary.")
		return nil
	}

	call, model, err := summarizeCaller(ctx)
	if err != nil {
		return err
	}
	layers := repoLayers(ctx, root, repoID)
	batches := (len(cands) + summarize.DefaultBatchSize - 1) / summarize.DefaultBatchSize
	fmt.Fprintf(w, "Summarizing %d files in %d batches via agy (model %s)...\n", len(cands), batches, model)

	items, rep, runErr := summarize.Run(ctx, call, cands, summarize.Options{Layers: layers, Model: model})
	if runErr != nil {
		// Partial results are still worth writing; the failure is reported
		// either as a warning (some landed) or as the error (none did).
		fmt.Fprintf(os.Stderr, "Warning: %v\n", runErr)
	}

	var res codemapops.SetSummariesResult
	if len(items) > 0 {
		indexer, indexerErr := openCodeMapIndexer()
		if indexerErr != nil {
			return indexerErr
		}
		if res, err = indexer.SetSummaries(ctx, repoID, items); err != nil {
			return fmt.Errorf("writing summaries: %w", err)
		}
		commandDidWrite.Store(true)
		if err := patchCache(beadsDir, cached, cacheErr, items, res.RefusedPaths); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: cache: %v\n", err)
		}
	}
	renderSummariesReport(w, rep, res)
	if res.Written == 0 {
		if runErr != nil {
			return runErr
		}
		return fmt.Errorf("no summaries written for %d candidate files", len(cands))
	}
	return nil
}

// summariesWriter keeps progress out of a --json payload's stdout.
func summariesWriter() io.Writer {
	if jsonOutput {
		return os.Stderr
	}
	return os.Stdout
}

// candidatePaths is the work list: the files whose summary no longer matches
// their contents first, then the ones that never had a summary. The cache is
// the only record of the second group — a never-summarized file is not stale.
func candidatePaths(stale []codemapops.NodeRef, cached cache.File) []string {
	var out []string
	seen := map[string]struct{}{}
	for _, s := range stale {
		if _, dup := seen[s.Path]; dup {
			continue
		}
		seen[s.Path] = struct{}{}
		out = append(out, s.Path)
	}
	var never []string
	for p, e := range cached.Files {
		if e.Summary != "" {
			continue
		}
		if _, dup := seen[p]; dup {
			continue
		}
		never = append(never, p)
	}
	sort.Strings(never)
	return append(out, never...)
}

// buildCandidates reads each path's head from disk and pins its current blob
// hash. A path with no blob hash is dropped: the store refuses a summary that
// is not pinned to the contents it describes.
//
// maxFiles caps the SURVIVORS, not the applicants: capping the path list first
// would let one untracked file at the head of it consume the whole budget and
// report "nothing to summarize" for a repository full of unsummarized files.
func buildCandidates(root string, paths []string, cached cache.File, maxFiles int) ([]summarize.Candidate, error) {
	if len(paths) == 0 {
		return nil, nil
	}
	blobs, err := scout.BlobHashes(root)
	if err != nil {
		return nil, err
	}
	// ONE scan over the candidates' directories for the package each file
	// belongs to; the alternative is a FileContext round trip per file.
	pkgOf := map[string]string{}
	if graphs, _, scanErr := scout.ScanAll(root, scout.PackageDirsFor(paths)); scanErr != nil {
		fmt.Fprintf(os.Stderr, "Warning: scout: %v (summarizing without package context)\n", scanErr)
	} else {
		for _, g := range graphs {
			for _, n := range g.Nodes {
				if n.Kind == codemapops.NodeFile {
					pkgOf[n.Path] = n.PackagePath
				}
			}
		}
	}
	out := make([]summarize.Candidate, 0, len(paths))
	for _, p := range paths {
		blob := blobs[p]
		if len(blob) != 40 {
			fmt.Fprintf(os.Stderr, "Warning: %s has no staged blob (untracked?); skipping\n", p)
			continue
		}
		// #nosec G304 -- a repo-relative path the code map already indexed
		src, readErr := os.ReadFile(filepath.Join(root, filepath.FromSlash(p)))
		if readErr != nil {
			fmt.Fprintf(os.Stderr, "Warning: reading %s: %v; skipping\n", p, readErr)
			continue
		}
		out = append(out, summarize.Candidate{
			Path: p, Package: pkgOf[p], BlobHash: blob,
			Head: summarize.Head(string(src)), Exported: summarize.Exported(string(src)),
			Importers: len(cached.Files[p].Importers),
		})
		if maxFiles > 0 && len(out) >= maxFiles {
			break
		}
	}
	return out, nil
}

// summarizeCaller returns the model call and the label to record with it. The
// fake is a file of canned JSON, read on every call, for tests and dry runs;
// without it the caller is agy, which must be on PATH before we announce a run.
func summarizeCaller(ctx context.Context) (summarize.Caller, string, error) {
	model := summariesModel(ctx)
	if fake := os.Getenv(fakeAgyEnv); fake != "" {
		return func(string) (string, error) {
			// #nosec G304 -- an operator-supplied path, the point of the knob
			data, err := os.ReadFile(fake)
			if err != nil {
				return "", fmt.Errorf("%s: %w", fakeAgyEnv, err)
			}
			return string(data), nil
		}, model, nil
	}
	if err := agyclient.Available(); err != nil {
		return nil, "", fmt.Errorf("`agy` not found on PATH, so summaries cannot be generated: %w", err)
	}
	return func(prompt string) (string, error) { return agyclient.Call(prompt, model, "") }, model, nil
}

// summariesModel resolves the label recorded on each row: config.yaml first,
// then the database, then agy's default — the same precedence the code map's
// other settings use.
func summariesModel(ctx context.Context) string {
	model := strings.TrimSpace(config.GetString(summariesModelKey))
	if model == "" && store != nil {
		if v, err := store.GetConfig(ctx, summariesModelKey); err == nil {
			model = strings.TrimSpace(v)
		}
	}
	if model == "" {
		model = agyclient.DefaultModel
	}
	if len(model) > maxModelLabel {
		model = model[:maxModelLabel]
	}
	return model
}

// repoLayers reads the repository's layer vocabulary, minting and storing it
// from the tree's own shape on first use.
func repoLayers(ctx context.Context, root, repoID string) []string {
	key := layersKeyFor(repoID)
	if store != nil {
		if v, err := store.GetConfig(ctx, key); err == nil {
			if layers := splitLayers(v); len(layers) > 0 {
				return layers
			}
		}
	}
	layers := summarize.DefaultLayers(root)
	if store != nil {
		if err := store.SetConfig(ctx, key, strings.Join(layers, ",")); err != nil {
			// A vocabulary we could not persist still summarizes this run; it
			// just gets recomputed next time.
			fmt.Fprintf(os.Stderr, "Warning: recording %s: %v\n", key, err)
		} else {
			commandDidWrite.Store(true)
		}
	}
	return layers
}

func splitLayers(v string) []string {
	var out []string
	for _, l := range strings.Split(v, ",") {
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, l)
		}
	}
	return out
}

// patchCache updates the cache entries for the paths that actually landed —
// items minus the store's refusals — and leaves every other entry alone. A
// whole rebuild would need a scan the summarizer has no reason to run.
func patchCache(beadsDir string, cached cache.File, cacheErr error, items []codemapops.Summary, refused []string) error {
	if cacheErr != nil {
		if !errors.Is(cacheErr, os.ErrNotExist) {
			return cacheErr
		}
		return nil // no cache yet: the next build writes one with these summaries in it
	}
	refusedSet := make(map[string]struct{}, len(refused))
	for _, p := range refused {
		refusedSet[p] = struct{}{}
	}
	if cached.Files == nil {
		cached.Files = map[string]cache.Entry{}
	}
	for _, it := range items {
		if _, no := refusedSet[it.Path]; no {
			continue
		}
		e, ok := cached.Files[it.Path]
		if !ok {
			e = cache.Entry{Path: it.Path}
		}
		e.Summary, e.Layer, e.Stale = it.Summary, it.Layer, false
		cached.Files[it.Path] = e
	}
	return cache.Write(beadsDir, cached)
}

// renderSummariesReport prints what the pass did, including WHY items were
// dropped: a model that keeps misspelling a layer is a fixable problem, and a
// bare count hides it.
func renderSummariesReport(w io.Writer, rep summarize.Report, res codemapops.SetSummariesResult) {
	fmt.Fprintf(w, "  %s %d written", ui.RenderPass("✓"), res.Written)
	if res.Refused > 0 {
		fmt.Fprintf(w, ", %d refused (contents changed under the summary)", res.Refused)
	}
	if rep.Dropped > 0 {
		fmt.Fprintf(w, ", %d dropped", rep.Dropped)
	}
	fmt.Fprintln(w)
	for _, p := range res.RefusedPaths {
		fmt.Fprintf(w, "    %s refused: %s\n", ui.RenderWarn("⚠"), p)
	}
	for _, reason := range summarizeReasons(rep.DroppedReasons) {
		fmt.Fprintf(w, "    %s dropped: %s\n", ui.RenderWarn("⚠"), reason)
	}
}

// summarizeReasons caps the dropped-reason list so a bad batch cannot bury the
// counts above it.
func summarizeReasons(reasons []string) []string {
	const max = 10
	if len(reasons) <= max {
		return reasons
	}
	return append(reasons[:max:max], fmt.Sprintf("... and %d more", len(reasons)-max))
}
