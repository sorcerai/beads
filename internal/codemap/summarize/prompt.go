package summarize

import (
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"

	"github.com/steveyegge/beads/internal/codemap/scout"
)

// headLines and headBytes bound the source excerpt one candidate contributes.
// Sixty non-blank lines is the imports, the types and the first function or
// two — enough to say what a file is for, small enough that a batch of 25 fits
// in a prompt.
const (
	headLines = 60
	headBytes = 3 << 10
	maxNames  = 20
)

// fixedLayers are the vocabulary every repository gets on top of its own
// top-level directories. A layer the model cannot spell is a dropped item, so
// the list stays short and generic.
var fixedLayers = []string{"cli", "domain", "integration", "storage", "test", "tooling"}

// Head returns the excerpt of src the prompt carries: its first headLines
// non-blank lines, truncated to headBytes.
func Head(src string) string {
	var b strings.Builder
	n := 0
	for _, line := range strings.Split(src, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		if b.Len()+len(line)+1 > headBytes {
			break
		}
		b.WriteString(line)
		b.WriteByte('\n')
		if n++; n >= headLines {
			break
		}
	}
	return b.String()
}

// exportedRE matches the top-level declarations the scouts count as exported:
// Go's capitalized func/type/var/const and Rust's `pub` items. It is a
// heuristic over text, not a parse — the head of the file is in the prompt
// anyway, and these names are a hint about what the file offers.
var exportedRE = regexp.MustCompile(`(?m)^(?:func \([^)]*\) ([A-Z]\w*)|func ([A-Z]\w*)|type ([A-Z]\w*)|(?:var|const) ([A-Z]\w*)|pub(?:\([^)]*\))? (?:async )?(?:fn|struct|enum|trait|type|const|static) (\w+))`)

// Exported names the exported declarations in src, capped at maxNames.
func Exported(src string) []string {
	var out []string
	seen := map[string]struct{}{}
	for _, m := range exportedRE.FindAllStringSubmatch(src, -1) {
		for _, name := range m[1:] {
			if name == "" {
				continue
			}
			if _, dup := seen[name]; dup {
				continue
			}
			seen[name] = struct{}{}
			out = append(out, name)
			if len(out) >= maxNames {
				return out
			}
		}
	}
	return out
}

// DefaultLayers is the layer vocabulary a repository starts with: its own
// top-level directories (minus the ones no scout indexes) plus the fixed set.
// It is persisted on first use, so renaming a directory later does not silently
// change what a summary may be labeled.
func DefaultLayers(root string) []string {
	set := map[string]struct{}{}
	for _, l := range fixedLayers {
		set[l] = struct{}{}
	}
	ig, err := scout.LoadIgnorer(root)
	entries, readErr := os.ReadDir(root)
	if err == nil && readErr == nil {
		for _, e := range entries {
			name := e.Name()
			if !e.IsDir() || strings.HasPrefix(name, ".") || ig.Skip(name+"/") {
				continue
			}
			set[name] = struct{}{}
		}
	}
	out := make([]string, 0, len(set))
	for l := range set {
		out = append(out, l)
	}
	sort.Strings(out)
	return out
}

// BuildPrompt asks for one JSON array and nothing else. Every constraint the
// parser enforces is stated here, because an item the model spells wrong is an
// item dropped, not an item retried.
func BuildPrompt(cands []Candidate, layers []string) string {
	var b strings.Builder
	b.WriteString(`You are summarizing source files for a code map. For EACH file below, write ONE
line saying what the file is for — what it owns, not what language it is in.

Output a JSON array and NOTHING else (no prose, no code fences):
[{"path":"<exact path from the list>","summary":"<= 160 chars, one sentence","layer":"<one of the layers>","tags":["<= 5 short tags"]}]

RULES:
- One entry per file, using the path EXACTLY as given.
- summary: <= 160 characters, no leading "This file"; say what it does.
- layer: exactly one of: `)
	b.WriteString(strings.Join(layers, ", "))
	b.WriteString("\n- tags: at most 5, lowercase, one word each. Omit rather than invent.\n")
	b.WriteString("- An entry that breaks any rule is discarded, so keep them strict.\n")
	for _, c := range cands {
		fmt.Fprintf(&b, "\n--- FILE %s\n", c.Path)
		if c.Package != "" {
			fmt.Fprintf(&b, "package: %s\n", c.Package)
		}
		fmt.Fprintf(&b, "importers: %d\n", c.Importers)
		if len(c.Exported) > 0 {
			fmt.Fprintf(&b, "exported: %s\n", strings.Join(c.Exported, ", "))
		}
		if c.Head != "" {
			fmt.Fprintf(&b, "head:\n%s", c.Head)
		}
	}
	return b.String()
}
