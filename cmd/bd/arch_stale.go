package main

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Deterministic ARCH.md staleness check (qb7.7).
//
// ARCH.md names code artifacts — file paths, package paths, backtick-quoted Go
// identifiers. When code moves and ARCH.md doesn't, the blueprint rots exactly
// the way it exists to prevent. This check parses ARCH.md for backtick-quoted
// references and verifies each still exists in the repo: paths via os.Stat
// (files and package directories), identifiers via a bounded word search over
// module-local .go files. Zero LLM, zero network.
//
// ADVISORY by default: findings print in their own section and do not fail the
// gate unless --strict is set. Staleness is a doc bug, not drift.

// archRefKind classifies a backtick-quoted ARCH.md reference.
type archRefKind int

const (
	archRefPath  archRefKind = iota // file or package path — check with os.Stat
	archRefIdent                    // Go identifier — check with a word search over .go files
)

// archRef is one code-artifact reference extracted from ARCH.md.
type archRef struct {
	token string // as written in ARCH.md (for the finding message)
	rel   string // repo-relative path to stat (path refs only)
	kind  archRefKind
}

var (
	archBacktickRe = regexp.MustCompile("`([^`\n]+)`")
	goIdentRe      = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	fileNameRe     = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._-]*\.[A-Za-z0-9]+$`)
)

// extractArchRefs parses ARCH.md content for backtick-quoted references to
// code artifacts. modulePath (from go.mod) lets fully-qualified package paths
// resolve to repo-relative directories. Tokens that don't look like a path or
// a Go identifier (commands with spaces, placeholders, flags) are skipped —
// the check errs toward silence, not false alarms.
func extractArchRefs(content, modulePath string) []archRef {
	seen := map[string]bool{}
	var refs []archRef
	for _, m := range archBacktickRe.FindAllStringSubmatch(content, -1) {
		token := strings.TrimSpace(m[1])
		if token == "" || seen[token] || strings.ContainsAny(token, " \t") {
			continue
		}
		seen[token] = true

		rel := token
		if modulePath != "" {
			if token == modulePath {
				continue // the module root is the repo root — always exists
			}
			if strings.HasPrefix(token, modulePath+"/") {
				rel = strings.TrimPrefix(token, modulePath+"/")
			}
		}
		switch {
		case strings.Contains(rel, "/"):
			// Path or package path. Skip glob/placeholder forms (cmd/*, <x>/y).
			if strings.ContainsAny(rel, "*<>|") {
				continue
			}
			refs = append(refs, archRef{token: token, rel: strings.TrimPrefix(rel, "./"), kind: archRefPath})
		case fileNameRe.MatchString(rel):
			// Bare filename with an extension (go.mod, ARCH.md) — a root-level path.
			refs = append(refs, archRef{token: token, rel: rel, kind: archRefPath})
		case goIdentRe.MatchString(rel) && len(rel) >= 3:
			// ponytail: length >= 3 skips noise like `go`/`bd`; raise if short
			// identifiers ever matter.
			refs = append(refs, archRef{token: token, kind: archRefIdent})
		}
	}
	return refs
}

// checkArchStaleness returns one finding per dangling ARCH.md reference, in
// ARCH.md order. Returns nil when ARCH.md is absent or fully fresh.
func checkArchStaleness(repoRoot string) []string {
	// #nosec G304 -- repoRoot is the resolved repo root; ARCH.md is a fixed name.
	data, err := os.ReadFile(filepath.Join(repoRoot, "ARCH.md"))
	if err != nil {
		return nil // no blueprint — nothing to be stale
	}
	modPath, _ := readGoModulePath(repoRoot)
	refs := extractArchRefs(string(data), modPath)

	// Resolve every reference to present/absent. Path refs (and ident tokens
	// that are really extension-less root files like LICENSE/Makefile) stat
	// directly; the rest need a whole-word search over the module's .go files,
	// batched into ONE walk below. extractArchRefs dedups tokens, so keying
	// exists by ref.token is unambiguous.
	exists := make(map[string]bool, len(refs))
	pending := map[string]bool{} // ident tokens still needing the walk
	for _, ref := range refs {
		switch ref.kind {
		case archRefPath:
			_, statErr := os.Stat(filepath.Join(repoRoot, filepath.FromSlash(ref.rel)))
			exists[ref.token] = statErr == nil
		case archRefIdent:
			// A bare capitalized token can be a Go symbol OR a root file with no
			// extension (LICENSE, Makefile, Dockerfile). Stat it first so those
			// don't read as stale just because they aren't Go identifiers.
			if _, statErr := os.Stat(filepath.Join(repoRoot, ref.token)); statErr == nil {
				exists[ref.token] = true
			} else {
				pending[ref.token] = true
			}
		}
	}
	resolveIdentsInGoFiles(repoRoot, pending, exists)

	var findings []string
	for _, ref := range refs {
		if !exists[ref.token] {
			findings = append(findings, "ARCH.md stale: references "+ref.token+" which no longer exists")
		}
	}
	return findings
}

// maxStaleGoFileSize bounds the per-file read for the identifier search.
const maxStaleGoFileSize = 2 << 20 // 2 MiB

// resolveIdentsInGoFiles walks the module's .go files ONCE, marking every
// pending identifier that occurs as a whole word (exists[token]=true) and
// removing it from pending. Stops early when pending empties; tokens never
// found stay absent. Bounded: skips .git/.beads/vendor/node_modules and
// oversized files. One walk for all idents instead of one walk per ident.
func resolveIdentsInGoFiles(root string, pending, exists map[string]bool) {
	if len(pending) == 0 {
		return
	}
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", ".beads", "vendor", "node_modules":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		if info, ierr := d.Info(); ierr != nil || info.Size() > maxStaleGoFileSize {
			return nil
		}
		// #nosec G304 -- path comes from WalkDir over the repo tree, not user input.
		data, rerr := os.ReadFile(path)
		if rerr != nil {
			return nil
		}
		for token := range pending {
			if containsWord(data, token) {
				exists[token] = true
				delete(pending, token) // safe to delete during range in Go
			}
		}
		if len(pending) == 0 {
			return filepath.SkipAll
		}
		return nil
	})
}

// containsWord reports whether word occurs in data with non-identifier bytes
// (or the buffer edge) on both sides.
func containsWord(data []byte, word string) bool {
	w := []byte(word)
	for i := 0; ; {
		j := bytes.Index(data[i:], w)
		if j < 0 {
			return false
		}
		j += i
		beforeOK := j == 0 || !isIdentByte(data[j-1])
		end := j + len(w)
		afterOK := end >= len(data) || !isIdentByte(data[end])
		if beforeOK && afterOK {
			return true
		}
		i = j + 1
	}
}

func isIdentByte(b byte) bool {
	return b == '_' || ('a' <= b && b <= 'z') || ('A' <= b && b <= 'Z') || ('0' <= b && b <= '9')
}
