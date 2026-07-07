package main

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// TestArchExtractRefs verifies backtick-quoted ARCH.md tokens are classified
// into path/identifier references (and noise is skipped).
func TestArchExtractRefs(t *testing.T) {
	t.Parallel()
	const mod = "example.com/mymod"
	tests := []struct {
		name    string
		content string
		want    []archRef
	}{
		{
			name:    "no backticks",
			content: "# ARCH.md\nplain prose, no references.\n",
			want:    nil,
		},
		{
			name:    "package path",
			content: "`internal/types` is the core.",
			want:    []archRef{{token: "internal/types", rel: "internal/types", kind: archRefPath}},
		},
		{
			name:    "module-qualified path is made repo-relative",
			content: "see `example.com/mymod/internal/storage`.",
			want:    []archRef{{token: "example.com/mymod/internal/storage", rel: "internal/storage", kind: archRefPath}},
		},
		{
			name:    "module root itself is skipped",
			content: "the root `example.com/mymod` package.",
			want:    nil,
		},
		{
			name:    "bare filename with extension",
			content: "config lives in `go.mod` and `ARCH.md`.",
			want: []archRef{
				{token: "go.mod", rel: "go.mod", kind: archRefPath},
				{token: "ARCH.md", rel: "ARCH.md", kind: archRefPath},
			},
		},
		{
			name:    "go identifier",
			content: "only `FooBar` may write.",
			want:    []archRef{{token: "FooBar", kind: archRefIdent}},
		},
		{
			name:    "commands, globs, placeholders skipped",
			content: "run `bd arch init`; never `cmd/*` nor `<core>/x` nor `--force` nor `go`.",
			want:    nil,
		},
		{
			name:    "duplicates collapsed",
			content: "`internal/types` and again `internal/types`.",
			want:    []archRef{{token: "internal/types", rel: "internal/types", kind: archRefPath}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := extractArchRefs(tt.content, mod)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("extractArchRefs() = %#v, want %#v", got, tt.want)
			}
		})
	}
}

// TestArchStaleness verifies end-to-end staleness verdicts against a t.TempDir
// fixture repo: existing paths/identifiers are fresh, dangling ones are flagged.
func TestArchStaleness(t *testing.T) {
	t.Parallel()

	// newFixture builds a minimal repo: go.mod, one package dir, one .go file
	// defining KeepMeAround.
	newFixture := func(t *testing.T, archMd string) string {
		t.Helper()
		root := t.TempDir()
		mustWrite := func(rel, content string) {
			path := filepath.Join(root, rel)
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		mustWrite("go.mod", "module example.com/mymod\n\ngo 1.22\n")
		mustWrite("internal/types/types.go", "package types\n\n// KeepMeAround and AlsoKept are referenced by ARCH.md.\nfunc KeepMeAround() {}\nfunc AlsoKept() {}\n")
		mustWrite("LICENSE", "MIT License\n") // extension-less root file, not a Go symbol
		mustWrite("ARCH.md", archMd)
		return root
	}

	tests := []struct {
		name   string
		archMd string
		want   []string
	}{
		{
			name:   "all references fresh",
			archMd: "`internal/types` holds core types; only `KeepMeAround` and `go.mod` matter.",
			want:   nil,
		},
		{
			name:   "dangling package path",
			archMd: "`internal/gone` must not import anything.",
			want:   []string{"ARCH.md stale: references internal/gone which no longer exists"},
		},
		{
			name:   "dangling module-qualified path",
			archMd: "see `example.com/mymod/internal/vanished`.",
			want:   []string{"ARCH.md stale: references example.com/mymod/internal/vanished which no longer exists"},
		},
		{
			name:   "dangling identifier",
			archMd: "only `RemovedGadget` may write to the store.",
			want:   []string{"ARCH.md stale: references RemovedGadget which no longer exists"},
		},
		{
			name:   "identifier substring does not count as a word hit",
			archMd: "`KeepMe` is not defined (only KeepMeAround is).",
			want:   []string{"ARCH.md stale: references KeepMe which no longer exists"},
		},
		{
			name:   "mixed fresh and stale, ARCH.md order",
			archMd: "`internal/types` then `internal/gone` then `RemovedGadget`.",
			want: []string{
				"ARCH.md stale: references internal/gone which no longer exists",
				"ARCH.md stale: references RemovedGadget which no longer exists",
			},
		},
		{
			// LICENSE matches the Go-identifier pattern but is really a root file;
			// stat must find it before the .go word search reports it stale.
			name:   "extension-less root file is fresh, not a stale identifier",
			archMd: "the `LICENSE` lives at the repo root.",
			want:   nil,
		},
		{
			// Two found + two dangling idents resolved in a single walk, findings
			// still in ARCH.md order.
			name:   "multiple identifiers resolved in one walk",
			archMd: "`KeepMeAround` and `AlsoKept` stay; `RemovedGadget` and `AlsoGone` are gone.",
			want: []string{
				"ARCH.md stale: references RemovedGadget which no longer exists",
				"ARCH.md stale: references AlsoGone which no longer exists",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			root := newFixture(t, tt.archMd)
			got := checkArchStaleness(root)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("checkArchStaleness() = %#v, want %#v", got, tt.want)
			}
		})
	}

	t.Run("no ARCH.md means no findings", func(t *testing.T) {
		t.Parallel()
		if got := checkArchStaleness(t.TempDir()); got != nil {
			t.Errorf("expected nil for repo without ARCH.md, got %#v", got)
		}
	})
}

// TestArchContainsWord pins the word-boundary matcher the identifier search uses.
func TestArchContainsWord(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		data string
		word string
		want bool
	}{
		{"exact word", "func FooBar() {}", "FooBar", true},
		{"prefix of longer ident", "func FooBarBaz() {}", "FooBar", false},
		{"suffix of longer ident", "func MyFooBar() {}", "FooBar", false},
		{"underscore joins", "foo_bar", "bar", false},
		{"at buffer edges", "FooBar", "FooBar", true},
		{"second occurrence matches", "FooBarBaz FooBar", "FooBar", true},
		{"absent", "nothing here", "FooBar", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := containsWord([]byte(tt.data), tt.word); got != tt.want {
				t.Errorf("containsWord(%q, %q) = %v, want %v", tt.data, tt.word, got, tt.want)
			}
		})
	}
}
