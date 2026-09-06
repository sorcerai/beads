package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/spf13/cobra"

	"github.com/steveyegge/beads/beadserrors"
	"github.com/steveyegge/beads/codemapops"
	"github.com/steveyegge/beads/internal/atomicfile"
	"github.com/steveyegge/beads/internal/codemap/cache"
)

func init() {
	codemapExportCmd.Flags().String("out", "", "Where to write the graph (default: "+uaGraphRelPath+" under the repository root)")
	codemapCmd.AddCommand(codemapExportCmd)
}

// uaGraphRelPath is where `bd explain` already looks for a knowledge graph, so
// exporting there is what makes the two halves of Task 22 one feature rather
// than two files that happen to share a shape.
var uaGraphRelPath = filepath.Join(".understand-anything", "knowledge-graph.json")

var codemapExportCmd = &cobra.Command{
	Use:           "export",
	Short:         "Export the code map as an Understand-Anything knowledge graph",
	Args:          cobra.NoArgs,
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, _ []string) error {
		out, _ := cmd.Flags().GetString("out")
		root, repoID, err := codemapRepoRoot()
		if err != nil {
			return HandleErrorRespectJSON("%v", err)
		}
		beadsDir, err := codemapBeadsDir()
		if err != nil {
			return HandleErrorRespectJSON("%v", err)
		}
		reader, err := openCodeMapReader()
		if err != nil {
			return HandleErrorRespectJSON("%v", err)
		}
		// The cache is the enumerator: the reader answers about one path at a
		// time and has no list-all, so the derived cache's key set is what
		// says which files the map holds — the same role `bd codemap build`
		// and `refresh` already give it.
		cached, err := cache.Read(beadsDir)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return HandleErrorRespectJSON("code map not built for this repository — run: bd codemap build")
			}
			return HandleErrorRespectJSON("reading the code map cache: %v", err)
		}

		graph, err := buildUAGraph(rootCtx, reader, repoID, filepath.Base(root), cached)
		if err != nil {
			return HandleErrorRespectJSON("%v", notIndexedHint(err))
		}
		if out == "" {
			out = filepath.Join(root, uaGraphRelPath)
		}
		if err := writeUAGraph(out, graph); err != nil {
			return HandleErrorRespectJSON("%v", err)
		}

		if jsonOutput {
			return outputJSON(map[string]any{
				"path": out, "nodes": len(graph.Nodes),
				"edges": len(graph.Edges), "layers": len(graph.Layers),
			})
		}
		fmt.Fprintf(os.Stdout, "Exported %d nodes, %d edges and %d layers to %s\n",
			len(graph.Nodes), len(graph.Edges), len(graph.Layers), out)
		return nil
	},
}

// uaNodeID is the graph-wide identifier for one map node. It carries the kind
// because a package import path and a file path share a namespace on the wire
// but not in the map, and an edge that named only the path could not say which
// of the two it meant.
func uaNodeID(kind codemapops.NodeKind, path string) string {
	if kind == codemapops.NodeFile {
		return "file:" + path
	}
	return "package:" + path
}

// buildUAGraph projects the indexed map into the Understand-Anything document.
//
// ponytail: one FileContext query per cached path — the same N-queries shape
// cache.BuildFromGraph documents as what made `bd codemap build` slow. Export
// is a manual, whole-repository verb rather than a hook, so N round trips is
// the honest cost; if it ever runs on a hot path the fix is a list-all on the
// reader role, not a second scan here.
func buildUAGraph(ctx context.Context, reader codemapops.Reader, repoID, projectName string, cached cache.File) (uaGraph, error) {
	paths := make([]string, 0, len(cached.Files))
	for p := range cached.Files {
		paths = append(paths, p)
	}
	sort.Strings(paths)

	nodes := map[string]uaNode{}
	layers := map[string][]string{}
	langs := map[string]bool{}
	edges := map[uaEdge]bool{}

	// ref registers a node seen only as an edge endpoint. First writer wins:
	// every spelling of one node comes from the same row, and the file loop
	// below writes the authoritative copy for the file it is on.
	ref := func(n codemapops.NodeRef) string {
		id := uaNodeID(n.Kind, n.Path)
		if _, seen := nodes[id]; !seen {
			nodes[id] = uaNodeOf(n, cache.Entry{})
		}
		if n.Lang != "" {
			langs[n.Lang] = true
		}
		return id
	}
	edge := func(src, dst, kind string) {
		edges[uaEdge{Source: src, Target: dst, Type: kind, Direction: "directed", Weight: 1}] = true
	}

	for _, path := range paths {
		fc, err := reader.FileContext(ctx, repoID, path)
		if err != nil {
			// A cache entry the map no longer holds is skipped rather than
			// fatal: the cache is a projection that can outlive a row, and an
			// export that died on one stale key would be unusable exactly when
			// the map most needs looking at.
			if errors.Is(err, beadserrors.ErrNotFound) {
				continue
			}
			return uaGraph{}, err
		}
		fileID := uaNodeID(codemapops.NodeFile, path)
		nodes[fileID] = uaNodeOf(fc.Node, cached.Files[path])
		if fc.Node.Lang != "" {
			langs[fc.Node.Lang] = true
		}
		if layer := uaLayerName(fc.Node, cached.Files[path]); layer != "" {
			layers[layer] = append(layers[layer], fileID)
		}

		var pkgID string
		if fc.Package.Path != "" {
			pkgID = ref(fc.Package)
			edge(pkgID, fileID, "contains")
		}
		for _, imp := range fc.Imports {
			edge(fileID, ref(imp), "imports")
		}
		// fc.Tests are the test files of this file's PACKAGE, which is the
		// edge the map actually holds: test file -> package.
		if pkgID != "" {
			for _, tst := range fc.Tests {
				edge(ref(tst), pkgID, "tests")
			}
		}
	}

	graph := uaGraph{
		Project: uaProject{Name: projectName, Languages: sortedKeys(langs)},
		Nodes:   make([]uaNode, 0, len(nodes)),
		Edges:   make([]uaEdge, 0, len(edges)),
		Layers:  make([]uaLayer, 0, len(layers)),
	}
	for _, id := range sortedKeys(nodes) {
		graph.Nodes = append(graph.Nodes, nodes[id])
	}
	for e := range edges {
		graph.Edges = append(graph.Edges, e)
	}
	sort.Slice(graph.Edges, func(i, j int) bool {
		a, b := graph.Edges[i], graph.Edges[j]
		if a.Type != b.Type {
			return a.Type < b.Type
		}
		if a.Source != b.Source {
			return a.Source < b.Source
		}
		return a.Target < b.Target
	})
	for _, name := range sortedKeys(layers) {
		ids := layers[name]
		sort.Strings(ids)
		graph.Layers = append(graph.Layers, uaLayer{ID: "layer:" + name, Name: name, NodeIds: ids})
	}
	return graph, nil
}

// uaNodeOf shapes one map node for the document. Two fields the schema carries
// have NO source on this read path and are exported empty rather than guessed:
// codemapops.NodeRef has no tags (the summarizer writes them, the reader does
// not return them) and no LOC, which is what the complexity bucket would be
// computed from.
func uaNodeOf(n codemapops.NodeRef, entry cache.Entry) uaNode {
	name := n.Name
	if name == "" {
		name = filepath.Base(n.Path)
	}
	out := uaNode{
		ID:      uaNodeID(n.Kind, n.Path),
		Type:    string(n.Kind),
		Name:    name,
		Summary: n.Summary,
	}
	if out.Summary == "" {
		out.Summary = entry.Summary
	}
	if n.Kind == codemapops.NodeFile {
		out.FilePath = n.Path
	}
	return out
}

func uaLayerName(n codemapops.NodeRef, entry cache.Entry) string {
	if n.Layer != "" {
		return n.Layer
	}
	return entry.Layer
}

// writeUAGraph replaces the document atomically. 0600 for the cache's reason:
// the summaries it carries quote source the repository may not publish.
func writeUAGraph(path string, graph uaGraph) error {
	data, err := json.MarshalIndent(graph, "", "  ")
	if err != nil {
		return err
	}
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return fmt.Errorf("creating %s: %w", dir, err)
		}
	}
	return atomicfile.WriteFile(path, append(data, '\n'), 0o600)
}
