package main

import "testing"

func TestIssueIDsInMessage(t *testing.T) {
	got := issueIDsInMessage("fix(x): thing (bd-abc12) closes bd-def34, not bdx-1 or foo-1", "bd")
	if len(got) != 2 || got[0] != "bd-abc12" || got[1] != "bd-def34" {
		t.Fatalf("%v", got)
	}
}

// TestIssueIDsInMessageAnchorsAndDedupes pins the two properties the regex is
// load-bearing for: it is anchored to THIS repository's prefix (a foreign
// prefix that merely starts with ours is not an id), and a message naming the
// same id twice records it once.
func TestIssueIDsInMessageAnchorsAndDedupes(t *testing.T) {
	got := issueIDsInMessage("cm-1 cm-1 cm-abc.2 cmx-9 xcm-1", "cm")
	if len(got) != 2 || got[0] != "cm-1" || got[1] != "cm-abc.2" {
		t.Fatalf("%v", got)
	}
	if ids := issueIDsInMessage("bd-abc12", "cm"); len(ids) != 0 {
		t.Fatalf("another repo's prefix must not match: %v", ids)
	}
}
