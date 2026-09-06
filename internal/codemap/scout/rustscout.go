package scout

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/steveyegge/beads/codemapops"
)

// RustScout maps a Cargo workspace: `cargo metadata` for the crate graph, and
// a line scan of each `src/**/*.rs` for `mod`/`use` module edges.
type RustScout struct{}

func init() { Register(RustScout{}) }

func (RustScout) Name() string { return "rust" }

func (RustScout) Detect(root string) bool {
	_, err := os.Stat(filepath.Join(root, "Cargo.toml"))
	return err == nil
}

type cargoPkg struct {
	Name         string `json:"name"`
	ManifestPath string `json:"manifest_path"`
	Dependencies []struct {
		Name string `json:"name"`
	} `json:"dependencies"`
}

// crate is one workspace member with the repo-relative location of its source.
type crate struct {
	name    string
	srcDir  string // repo-relative, forward slashes
	deps    []string
	scanned bool // false when an incremental scan left this crate out of scope
}

var (
	modRE = regexp.MustCompile(`^\s*(?:pub(?:\([^)]*\))?\s+)?mod\s+([A-Za-z_][A-Za-z0-9_]*)\s*;`)
	useRE = regexp.MustCompile(`^\s*(?:pub(?:\([^)]*\))?\s+)?use\s+([A-Za-z_][A-Za-z0-9_:]*)`)
)

// Scan reads the Cargo workspace at root. only lists crate names to rescan;
// nil scans every workspace member.
func (RustScout) Scan(root string, only []string) (codemapops.Graph, error) {
	if _, err := exec.LookPath("cargo"); err != nil {
		return codemapops.Graph{}, &ErrToolMissing{Tool: "cargo"}
	}
	pkgs, err := workspacePackages(root)
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

	// byModule maps the identifier a `use` writes (hyphens become underscores)
	// to the crate, so member paths resolve and external crates do not.
	byModule := map[string]*crate{}
	members := map[string]bool{}
	var crates []*crate
	wanted := map[string]bool{}
	for _, c := range only {
		wanted[c] = true
	}
	for _, p := range pkgs {
		members[p.Name] = true
		dir, err := relTo(root, filepath.Dir(p.ManifestPath))
		if err != nil {
			continue
		}
		c := &crate{name: p.Name, srcDir: path.Join(dir, "src")}
		for _, d := range p.Dependencies {
			c.deps = append(c.deps, d.Name)
		}
		byModule[strings.ReplaceAll(p.Name, "-", "_")] = c
		if only == nil || wanted[p.Name] || wantedDir(wanted, path.Dir(c.srcDir)) {
			c.scanned = true
			crates = append(crates, c)
		}
	}

	g := codemapops.Graph{Lang: "rust"}
	if only != nil {
		// The crate names this scope matched. A workspace member whose
		// directory is gone is no longer in `cargo metadata` either, so it
		// cannot be named here — see Graph.ScopedPackages.
		g.ScopedPackages = make([]string, 0, len(crates))
		for _, c := range crates {
			g.ScopedPackages = append(g.ScopedPackages, c.name)
		}
		sort.Strings(g.ScopedPackages)
	}
	// Pass 1: crate nodes, crate->crate edges, and every indexable file, so
	// pass 2 can resolve a reference only to a file the map actually holds.
	fileOf := map[string]*crate{}
	for _, c := range crates {
		g.Nodes = append(g.Nodes, codemapops.Node{
			Kind: codemapops.NodePackage, Path: c.name, Name: c.name, Lang: "rust",
		})
		deps := append([]string(nil), c.deps...)
		sort.Strings(deps)
		for _, d := range deps {
			if members[d] && d != c.name {
				g.Edges = append(g.Edges, codemapops.Edge{
					Src: c.name, Dst: d, Kind: codemapops.EdgeImports, Weight: 1,
				})
			}
		}
		files, err := rustFiles(root, c.srcDir, ig)
		if err != nil {
			return g, err
		}
		for _, rel := range files {
			// #nosec G304 -- path comes from walking the scanned repo
			src, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
			if err != nil {
				return g, err
			}
			g.Nodes = append(g.Nodes, codemapops.Node{
				Kind: codemapops.NodeFile, Path: rel, Name: path.Base(rel), PackagePath: c.name, Lang: "rust",
				BlobHash: blobs[rel], LOC: countNonBlank(src), Exported: countRustPub(src),
				IsTest: strings.Contains(rel, "/tests/") || strings.Contains(string(src), "#[cfg(test)]"),
			})
			g.Edges = append(g.Edges, codemapops.Edge{
				Src: c.name, Dst: rel, Kind: codemapops.EdgeContains, Weight: 1,
			})
			fileOf[rel] = c
		}
	}

	// Pass 2: module references. A reference into our own workspace that names
	// no indexed file is counted, never guessed at.
	indexed := make(map[string]bool, len(fileOf))
	for rel := range fileOf {
		indexed[rel] = true
	}
	rels := make([]string, 0, len(fileOf))
	for rel := range fileOf {
		rels = append(rels, rel)
	}
	sort.Strings(rels)
	for _, rel := range rels {
		// #nosec G304 -- path comes from walking the scanned repo
		src, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil {
			return g, err
		}
		c := fileOf[rel]
		for _, line := range strings.Split(string(src), "\n") {
			var target string
			var ours bool
			if m := modRE.FindStringSubmatch(line); m != nil {
				target, ours = resolveModule(indexed, moduleDir(rel), m[1]), true
			} else if m := useRE.FindStringSubmatch(line); m != nil {
				target, ours = resolveUse(indexed, byModule, c, rel, m[1])
			}
			if !ours {
				continue
			}
			if target == "" {
				g.Dropped++
				continue
			}
			if target != rel {
				g.Edges = append(g.Edges, codemapops.Edge{
					Src: rel, Dst: target, Kind: codemapops.EdgeImports, Weight: 1,
				})
			}
		}
	}
	return resolveEdges(g), nil
}

// wantedDir reports whether any requested scope names crateDir or something
// inside it, so a caller may ask for a crate by repo-relative directory (what
// PackageDirsFor produces) as well as by crate name.
func wantedDir(wanted map[string]bool, crateDir string) bool {
	for w := range wanted {
		if crateDir == "." || w == crateDir || strings.HasPrefix(w, crateDir+"/") {
			return true
		}
	}
	return false
}

// workspacePackages returns the workspace members' manifests. --no-deps keeps
// the answer to crates we own, and avoids a network fetch.
func workspacePackages(root string) ([]cargoPkg, error) {
	cmd := exec.Command("cargo", "metadata", "--format-version", "1", "--no-deps")
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("cargo metadata: %w", err)
	}
	var meta struct {
		Packages []cargoPkg `json:"packages"`
	}
	if err := json.Unmarshal(out, &meta); err != nil {
		return nil, fmt.Errorf("cargo metadata: %w", err)
	}
	return meta.Packages, nil
}

// rustFiles lists the indexable .rs files under a crate's src directory, as
// sorted repo-relative paths.
func rustFiles(root, srcDir string, ig *Ignorer) ([]string, error) {
	var out []string
	base := filepath.Join(root, filepath.FromSlash(srcDir))
	err := filepath.WalkDir(base, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil // a crate with no src/ is not an error
			}
			return err
		}
		rel, relErr := filepath.Rel(root, p)
		if relErr != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			if ig.Skip(rel + "/") {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(p, ".rs") && !ig.Skip(rel) {
			out = append(out, rel)
		}
		return nil
	})
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	sort.Strings(out)
	return out, nil
}

// moduleDir returns the directory a file's child modules live in: the file's
// own directory for a crate or module root, otherwise a directory named after
// the file.
func moduleDir(rel string) string {
	stem := strings.TrimSuffix(path.Base(rel), ".rs")
	switch stem {
	case "lib", "main", "mod":
		return path.Dir(rel)
	default:
		return path.Join(path.Dir(rel), stem)
	}
}

// resolveModule maps a module name inside dir to an indexed file, preferring
// `dir/name.rs` over `dir/name/mod.rs`.
func resolveModule(indexed map[string]bool, dir, name string) string {
	for _, cand := range []string{path.Join(dir, name+".rs"), path.Join(dir, name, "mod.rs")} {
		if indexed[cand] {
			return cand
		}
	}
	return ""
}

// resolveUse maps a `use` path to an indexed file. ours reports whether the
// path points at a crate this scan actually read; a std, third-party, or
// out-of-scope crate is neither an edge nor a drop.
func resolveUse(indexed map[string]bool, byModule map[string]*crate, c *crate, rel, usePath string) (target string, ours bool) {
	segs := strings.Split(usePath, "::")
	if len(segs) == 0 {
		return "", false
	}
	var dir string
	switch segs[0] {
	case "crate":
		dir = c.srcDir
	case "super":
		md := moduleDir(rel)
		if md == c.srcDir {
			dir = c.srcDir
		} else {
			dir = path.Dir(md)
		}
	default:
		other, ok := byModule[segs[0]]
		if !ok {
			return "", false // std or a third-party crate
		}
		if !other.scanned {
			// Out of this scan's scope, not unresolvable: the crate's files
			// were never indexed, so there is nothing here to fail to find.
			return "", false
		}
		dir = other.srcDir
	}
	rest := segs[1:]
	if len(rest) == 0 {
		return "", false // names the crate itself; the crate edge says that
	}
	// Take the deepest segment that names a real file.
	for _, seg := range rest {
		f := resolveModule(indexed, dir, seg)
		if f == "" {
			break
		}
		target = f
		dir = path.Join(dir, seg)
	}
	return target, true
}

// countRustPub counts a file's public items by their `pub` line prefix.
func countRustPub(src []byte) int {
	n := 0
	for _, line := range strings.Split(string(src), "\n") {
		if strings.HasPrefix(line, "pub ") {
			n++
		}
	}
	return n
}

// The two helpers below render a workspace as prompt text for `bd arch draft`.
// They read manifests directly rather than through cargo metadata, which is
// what keeps that command usable when cargo is absent.

// WorkspaceMembers reads the [workspace] members from the root Cargo.toml.
func WorkspaceMembers(repoRoot string) string {
	// #nosec G304 -- fixed manifest name under a caller-supplied repo root
	data, err := os.ReadFile(filepath.Join(repoRoot, "Cargo.toml"))
	if err != nil {
		return "(could not read root Cargo.toml)"
	}
	content := string(data)
	start := strings.Index(content, "[workspace]")
	if start < 0 {
		return "(no [workspace] table — single crate)"
	}
	rest := content[start:]
	end := strings.Index(rest, "\n[")
	if end < 0 {
		end = len(rest)
	}
	return rest[:end]
}

// InternalDeps scans each crate's Cargo.toml for reverie-*/cortex deps.
func InternalDeps(repoRoot string) string {
	cratesDir := filepath.Join(repoRoot, "crates")
	entries, err := os.ReadDir(cratesDir)
	if err != nil {
		return "(no crates/ dir)"
	}
	var b strings.Builder
	re := regexp.MustCompile(`(?m)^(reverie-[a-z0-9-]+|cortex[a-z0-9-]*)\b`)
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		ct := filepath.Join(cratesDir, e.Name(), "Cargo.toml")
		// #nosec G304 -- path is built from crates/ entries in the scanned repo
		data, err := os.ReadFile(ct)
		if err != nil {
			continue
		}
		matches := re.FindAllString(string(data), -1)
		// dedup
		seen := map[string]bool{}
		var deps []string
		for _, m := range matches {
			if m == e.Name() || seen[m] {
				continue
			}
			seen[m] = true
			deps = append(deps, m)
		}
		if len(deps) > 0 {
			sort.Strings(deps)
			b.WriteString(fmt.Sprintf("%s -> %s\n", e.Name(), strings.Join(deps, ", ")))
		}
	}
	return b.String()
}
