package scout

import (
	"bytes"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/steveyegge/beads/codemapops"
)

// GoScout maps a Go module: `go list -json` for the package graph, go/parser
// for per-file imports and exported-symbol counts.
type GoScout struct{}

func init() { Register(GoScout{}) }

func (GoScout) Name() string { return "go" }

func (GoScout) Detect(root string) bool {
	_, err := os.Stat(filepath.Join(root, "go.mod"))
	return err == nil
}

// ModulePath returns the module path declared by root's go.mod.
func ModulePath(root string) (string, error) {
	// #nosec G304 -- fixed file name under a caller-supplied repo root
	data, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "module ") {
			return strings.TrimSpace(strings.TrimPrefix(line, "module ")), nil
		}
	}
	return "", nil
}

// goListCmd builds the `go list` invocation a scan reads its package graph
// from, with the environment pinned: GOPROXY=off because a code map is a read
// of the tree in front of it and must never reach the network to resolve a
// dependency, and GOTOOLCHAIN=local because a go.mod naming a newer toolchain
// would otherwise make an indexing run download one. Both turn a slow surprise
// into a plain `go list` error the caller already reports.
func goListCmd(root string, patterns []string) *exec.Cmd {
	cmd := exec.Command("go", append([]string{"list", "-json", "-e"}, patterns...)...) // #nosec G204 -- patterns are import paths from the caller's own repo
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "GOPROXY=off", "GOTOOLCHAIN=local")
	return cmd
}

type goListPkg struct {
	ImportPath, Dir                    string
	GoFiles, TestGoFiles, XTestGoFiles []string
	Imports                            []string
}

// Scan reads the Go module at root. only lists import paths to rescan; nil
// scans the whole module.
func (GoScout) Scan(root string, only []string) (codemapops.Graph, error) {
	if _, err := exec.LookPath("go"); err != nil {
		return codemapops.Graph{}, &ErrToolMissing{Tool: "go"}
	}
	mod, err := ModulePath(root)
	if err != nil {
		return codemapops.Graph{}, err
	}
	ig, err := LoadIgnorer(root)
	if err != nil {
		return codemapops.Graph{}, err
	}
	blobs, err := BlobHashes(root)
	if err != nil {
		return codemapops.Graph{}, err
	}
	if only != nil && len(only) == 0 {
		return codemapops.Graph{Lang: "go", ScopedPackages: []string{}}, nil // nothing in scope
	}
	patterns := []string{"./..."}
	var scoped []string
	if only != nil {
		patterns = make([]string, 0, len(only))
		for _, o := range only {
			patterns = append(patterns, goPattern(mod, o))
			scoped = append(scoped, goScopeImportPath(mod, o))
		}
		sort.Strings(scoped)
	}
	out, err := goListCmd(root, patterns).Output()
	if err != nil {
		return codemapops.Graph{}, fmt.Errorf("go list: %w", err)
	}

	g := codemapops.Graph{Lang: "go", ScopedPackages: scoped}
	dec := json.NewDecoder(bytes.NewReader(out))
	fset := token.NewFileSet()
	// isInternal reports whether an import path belongs to this module.
	isInternal := func(imp string) bool { return imp == mod || strings.HasPrefix(imp, mod+"/") }

	for {
		var p goListPkg
		if err := dec.Decode(&p); err == io.EOF {
			break
		} else if err != nil {
			return g, err
		}
		if p.Dir == "" || p.ImportPath == "" {
			continue // -e placeholder for a pattern that resolved to nothing
		}
		rel, err := relTo(root, p.Dir)
		if err != nil {
			continue
		}
		if ig.Skip(rel + "/") {
			continue
		}
		g.Nodes = append(g.Nodes, codemapops.Node{
			Kind: codemapops.NodePackage, Path: p.ImportPath, Name: p.ImportPath, Lang: "go",
		})
		for _, dst := range internalImports(p.Imports, isInternal) {
			g.Edges = append(g.Edges, codemapops.Edge{
				Src: p.ImportPath, Dst: dst.path, Kind: codemapops.EdgeImports, Weight: dst.weight,
			})
		}

		files := append(append(append([]string{}, p.GoFiles...), p.TestGoFiles...), p.XTestGoFiles...)
		for _, f := range files {
			relFile := path.Join(rel, f)
			if ig.Skip(relFile) {
				continue
			}
			full := filepath.Join(p.Dir, f)
			// #nosec G304 -- path comes from go list within the scanned repo
			src, err := os.ReadFile(full)
			if err != nil {
				return g, err
			}
			if IsGenerated(src) {
				continue
			}
			parsed, err := parser.ParseFile(fset, full, src, parser.SkipObjectResolution)
			if err != nil {
				continue // unparsable file: skipped, never guessed at
			}
			isTest := strings.HasSuffix(f, "_test.go")
			g.Nodes = append(g.Nodes, codemapops.Node{
				Kind: codemapops.NodeFile, Path: relFile, Name: f, PackagePath: p.ImportPath, Lang: "go",
				BlobHash: blobs[relFile], LOC: countNonBlank(src), IsTest: isTest, Exported: countExported(parsed),
			})
			g.Edges = append(g.Edges, codemapops.Edge{
				Src: p.ImportPath, Dst: relFile, Kind: codemapops.EdgeContains, Weight: 1,
			})
			imps := make([]string, 0, len(parsed.Imports))
			for _, imp := range parsed.Imports {
				imps = append(imps, strings.Trim(imp.Path.Value, `"`))
			}
			for _, dst := range internalImports(imps, isInternal) {
				if isTest && dst.path == p.ImportPath {
					continue // the tests edge already says this
				}
				g.Edges = append(g.Edges, codemapops.Edge{
					Src: relFile, Dst: dst.path, Kind: codemapops.EdgeImports, Weight: dst.weight,
				})
			}
			if isTest {
				g.Edges = append(g.Edges, codemapops.Edge{
					Src: relFile, Dst: p.ImportPath, Kind: codemapops.EdgeTests, Weight: 1,
				})
			}
		}
	}
	return resolveEdges(g), nil
}

// goPattern turns one `only` entry into a `go list` pattern. Callers name a
// package either by import path or, as PackageDirsFor produces, by
// repo-relative directory; only the latter needs the "./" that tells `go list`
// it is a path and not an import path.
func goPattern(mod, only string) string {
	if only == mod || strings.HasPrefix(only, mod+"/") || strings.HasPrefix(only, ".") {
		return only
	}
	return "./" + only
}

// goScopeImportPath names the package one `only` entry refers to, WITHOUT
// asking the toolchain — a scope whose directory has been deleted still has to
// name the package whose rows the apply must remove, and `go list -e` answers
// such a pattern with a placeholder carrying no import path at all.
func goScopeImportPath(mod, only string) string {
	if only == mod || strings.HasPrefix(only, mod+"/") {
		return only
	}
	dir := path.Clean(strings.TrimPrefix(only, "./"))
	if dir == "." || dir == "" {
		return mod
	}
	return mod + "/" + dir
}

type weighted struct {
	path   string
	weight int
}

// internalImports counts the module-local imports in imps, sorted by path so
// the edge order a scan produces is stable.
func internalImports(imps []string, isInternal func(string) bool) []weighted {
	counts := map[string]int{}
	for _, imp := range imps {
		if isInternal(imp) {
			counts[imp]++
		}
	}
	out := make([]weighted, 0, len(counts))
	for p, w := range counts {
		out = append(out, weighted{p, w})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].path < out[j].path })
	return out
}

// resolveEdges merges duplicate (Src,Dst,Kind) edges by summing weight, drops
// edges whose file endpoint is not in the graph, and declares as external any
// package an edge imports that this scan did not visit — an incremental scan
// legitimately imports packages outside its scope.
func resolveEdges(g codemapops.Graph) codemapops.Graph {
	known := make(map[string]struct{}, len(g.Nodes))
	for _, n := range g.Nodes {
		known[n.Path] = struct{}{}
	}
	external := map[string]struct{}{}
	merged := make([]codemapops.Edge, 0, len(g.Edges))
	index := map[string]int{}
	for _, e := range g.Edges {
		if _, ok := known[e.Src]; !ok {
			continue
		}
		if _, ok := known[e.Dst]; !ok {
			// A contains edge names a file; an unknown file is a dropped
			// endpoint. Every other kind names a package — including the Rust
			// scout's file-to-file imports, which it emits only for files it
			// has already indexed, so an unknown Dst here is never a file.
			if e.Kind == codemapops.EdgeContains {
				continue
			}
			external[e.Dst] = struct{}{}
		}
		key := e.Src + "\x00" + e.Dst + "\x00" + string(e.Kind)
		if i, seen := index[key]; seen {
			merged[i].Weight += e.Weight
			continue
		}
		index[key] = len(merged)
		merged = append(merged, e)
	}
	g.Edges = merged
	if len(external) > 0 {
		g.ExternalPackages = make([]string, 0, len(external))
		for p := range external {
			g.ExternalPackages = append(g.ExternalPackages, p)
		}
		sort.Strings(g.ExternalPackages)
	}
	return g
}

// countNonBlank counts lines carrying any non-space character.
func countNonBlank(src []byte) int {
	n := 0
	for _, line := range strings.Split(string(src), "\n") {
		if strings.TrimSpace(line) != "" {
			n++
		}
	}
	return n
}

// countExported counts a file's exported top-level declarations: functions
// without a receiver, plus the named specs of any general declaration.
func countExported(f *ast.File) int {
	n := 0
	for _, decl := range f.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			if d.Recv == nil && d.Name.IsExported() {
				n++
			}
		case *ast.GenDecl:
			for _, spec := range d.Specs {
				switch s := spec.(type) {
				case *ast.TypeSpec:
					if s.Name.IsExported() {
						n++
					}
				case *ast.ValueSpec:
					for _, name := range s.Names {
						if name.IsExported() {
							n++
						}
					}
				}
			}
		}
	}
	return n
}
