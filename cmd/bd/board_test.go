package main

import (
	"context"
	"testing"

	"github.com/steveyegge/beads/internal/rollup"
)

func TestBuildBoardOptions(t *testing.T) {
	o := buildBoardOptions("alpha", 50)
	if o.Project != "alpha" || o.Limit != 50 {
		t.Fatalf("unexpected options: %#v", o)
	}
	d := buildBoardOptions("", 0)
	if d.Limit != 0 { // 0 => rollup.DefaultLimit applied downstream
		t.Fatalf("default limit should pass through as 0, got %d", d.Limit)
	}
}

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
