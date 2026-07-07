package main

import (
	"testing"
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
