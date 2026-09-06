// Package cache holds the derived, disposable JSON view of a repository's code
// map that the agent tool hooks read. It is a projection of the indexed graph,
// never a source of truth: delete it and `bd codemap` rebuilds it.
package cache

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/steveyegge/beads/codemapops"
	"github.com/steveyegge/beads/internal/atomicfile"
)

// FileName is the cache's name inside the .beads directory.
const FileName = "codemap.cache.json"

// maxNames caps the import and importer lists on one entry. The cache is a
// prompt-sized hint, not a complete edge list; the graph still has the rest.
const maxNames = 12

// Entry is one file's cached context.
type Entry struct {
	Path       string                `json:"path"`
	Summary    string                `json:"summary,omitempty"`
	Layer      string                `json:"layer,omitempty"`
	Stale      bool                  `json:"stale,omitempty"`
	Imports    []string              `json:"imports,omitempty"`
	Importers  []string              `json:"importers,omitempty"`
	OpenIssues []codemapops.IssueRef `json:"open_issues,omitempty"`
}

// File is the whole cache document, stamped with the commit it describes so a
// reader can tell whether it still applies.
type File struct {
	GeneratedAt time.Time        `json:"generated_at"`
	HeadSHA     string           `json:"head_sha"`
	RepoID      string           `json:"repo_id"`
	Files       map[string]Entry `json:"files"`
}

// Path returns the cache's location inside beadsDir.
func Path(beadsDir string) string { return filepath.Join(beadsDir, FileName) }

// Write replaces the cache atomically. 0600 because the summaries quote source
// the repository may not publish.
func Write(beadsDir string, f File) error {
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	return atomicfile.WriteFile(Path(beadsDir), append(data, '\n'), 0o600)
}

// Read loads the cache. A missing file returns an error satisfying
// os.ErrNotExist; corrupt JSON is an error, never a silently empty cache.
func Read(beadsDir string) (File, error) {
	// #nosec G304 -- fixed file name under a caller-supplied beads directory
	data, err := os.ReadFile(Path(beadsDir))
	if err != nil {
		return File{}, err
	}
	var f File
	if err := json.Unmarshal(data, &f); err != nil {
		return File{}, fmt.Errorf("%s: %w", Path(beadsDir), err)
	}
	return f, nil
}

// BuildFromGraph projects a freshly scanned graph into a cache document,
// deriving each file's imports and importers from the graph's own edges.
//
// It reads NOTHING per file. The previous shape asked the store for one
// FileContext and one ByPath per path, which is two transactions per file and
// what made `bd codemap build` spend minutes on a scan that takes seconds.
// Everything it still needs from the store arrives whole: prev for the
// summaries a scan does not carry, stale for which of them have gone out of
// date, and links for the open issues per path.
//
// Entries in prev whose path is NOT a file node in g are carried forward,
// because an incremental refresh only rescans some packages. A whole-repository
// build passes a prev already reduced to the graph's own paths, so a file that
// left the tree leaves the cache with it.
func BuildFromGraph(_ context.Context, repoID, headSHA string, g codemapops.Graph, prev File, stale []codemapops.NodeRef, links map[string][]codemapops.IssueRef) File {
	staleSet := make(map[string]struct{}, len(stale))
	for _, s := range stale {
		staleSet[s.Path] = struct{}{}
	}
	// file -> packages it imports, and package -> whatever imports it.
	imports := map[string][]string{}
	importersOf := map[string][]string{}
	for _, e := range g.Edges {
		if e.Kind != codemapops.EdgeImports {
			continue
		}
		imports[e.Src] = append(imports[e.Src], e.Dst)
		importersOf[e.Dst] = append(importersOf[e.Dst], e.Src)
	}

	out := File{
		GeneratedAt: time.Now().UTC(),
		HeadSHA:     headSHA,
		RepoID:      repoID,
		Files:       make(map[string]Entry, len(prev.Files)+len(g.Nodes)),
	}
	scanned := make(map[string]struct{}, len(g.Nodes))
	for _, n := range g.Nodes {
		if n.Kind == codemapops.NodeFile {
			scanned[n.Path] = struct{}{}
		}
	}
	for p, e := range prev.Files {
		if _, rescanned := scanned[p]; !rescanned {
			out.Files[p] = e
		}
	}
	for _, n := range g.Nodes {
		if n.Kind != codemapops.NodeFile {
			continue
		}
		entry := Entry{Path: n.Path}
		// A scan carries no summary; only a summarizer pass writes one, so the
		// previous entry is the only place it can come from.
		if old, ok := prev.Files[n.Path]; ok {
			entry.Summary, entry.Layer = old.Summary, old.Layer
		}
		_, entry.Stale = staleSet[n.Path]
		entry.Imports = capNames(imports[n.Path])
		entry.Importers = capNames(importersOf[n.PackagePath])
		entry.OpenIssues = links[n.Path]
		out.Files[n.Path] = entry
	}
	return out
}

// capNames sorts for a stable cache and caps the list at maxNames.
func capNames(names []string) []string {
	if len(names) == 0 {
		return nil
	}
	out := append([]string(nil), names...)
	sort.Strings(out)
	if len(out) > maxNames {
		out = out[:maxNames]
	}
	return out
}
