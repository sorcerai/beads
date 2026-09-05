// Package cache holds the derived, disposable JSON view of a repository's code
// map that the agent tool hooks read. It is a projection of the indexed graph,
// never a source of truth: delete it and `bd codemap` rebuilds it.
package cache

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/steveyegge/beads/beadserrors"
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

// Build projects the indexed map for paths into a cache document. A path the
// map does not know yields a bare entry rather than failing the whole build,
// because a caller's changed-file list routinely names new or ignored files.
func Build(ctx context.Context, repoID, headSHA string, reader codemapops.Reader, files codemapops.IssueFiles, paths []string) (File, error) {
	out := File{
		GeneratedAt: time.Now().UTC(),
		HeadSHA:     headSHA,
		RepoID:      repoID,
		Files:       make(map[string]Entry, len(paths)),
	}
	for _, p := range paths {
		if _, done := out.Files[p]; done {
			continue
		}
		entry := Entry{Path: p}
		fc, err := reader.FileContext(ctx, repoID, p)
		switch {
		case err == nil:
			entry.Summary = fc.Node.Summary
			entry.Layer = fc.Node.Layer
			entry.Stale = fc.Node.Stale
			entry.Imports = nodePaths(fc.Imports)
			entry.Importers = nodePaths(fc.Importers)
		case errors.Is(err, beadserrors.ErrNotFound):
			// Not indexed: the bare entry is the honest answer.
		default:
			return File{}, fmt.Errorf("file context for %s: %w", p, err)
		}
		issues, err := files.ByPath(ctx, repoID, p, true)
		if err != nil && !errors.Is(err, beadserrors.ErrNotFound) {
			return File{}, fmt.Errorf("open issues for %s: %w", p, err)
		}
		entry.OpenIssues = issues
		out.Files[p] = entry
	}
	return out, nil
}

// nodePaths flattens refs to their paths, capped at maxNames.
func nodePaths(refs []codemapops.NodeRef) []string {
	if len(refs) > maxNames {
		refs = refs[:maxNames]
	}
	out := make([]string, 0, len(refs))
	for _, r := range refs {
		out = append(out, r.Path)
	}
	return out
}
