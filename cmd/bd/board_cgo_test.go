//go:build cgo

package main

import (
	"context"
	"testing"

	"github.com/steveyegge/beads/internal/rollup"
)

func TestRunBoardJSON_EmptyStore(t *testing.T) {
	dbPath := t.TempDir() + "/bd"
	ts := newTestStore(t, dbPath)
	defer ts.Close()

	r, err := rollup.Compute(context.Background(), ts, rollup.Options{})
	if err != nil {
		t.Fatalf("Compute on empty store: %v", err)
	}
	if projectSlugPresent(r, "Unassigned") == false {
		t.Fatalf("empty store must still emit Unassigned bucket")
	}
}

func projectSlugPresent(r *rollup.Rollup, slug string) bool {
	for _, p := range r.Projects {
		if p.Slug == slug {
			return true
		}
	}
	return false
}
