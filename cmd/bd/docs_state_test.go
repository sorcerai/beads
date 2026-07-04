package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDocsStateRoundtrip(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	p := filepath.Join(dir, ".docs-state")

	// Missing file: ok=false.
	if _, ok := readDocsState(p); ok {
		t.Fatal("expected ok=false for missing state file")
	}

	want := docsState{RegenWatermark: time.Date(2026, 7, 4, 5, 0, 0, 0, time.UTC), Dirty: 12}
	if err := writeDocsState(p, want); err != nil {
		t.Fatalf("write: %v", err)
	}
	got, ok := readDocsState(p)
	if !ok || !got.RegenWatermark.Equal(want.RegenWatermark) || got.Dirty != want.Dirty {
		t.Fatalf("roundtrip mismatch: ok=%v got=%+v want=%+v", ok, got, want)
	}

	// Deterministic bytes: two writes of the same state are identical.
	b1, _ := os.ReadFile(p)
	if err := writeDocsState(p, want); err != nil {
		t.Fatalf("rewrite: %v", err)
	}
	b2, _ := os.ReadFile(p)
	if string(b1) != string(b2) {
		t.Fatal("state file bytes not deterministic")
	}

	// Corrupt file: ok=false.
	if err := os.WriteFile(p, []byte("not: [valid\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, ok := readDocsState(p); ok {
		t.Fatal("expected ok=false for corrupt state file")
	}
}
