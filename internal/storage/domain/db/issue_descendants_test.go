package db

import (
	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/types"
)

// bd-6dnrw.44 item 11: the descendants CTE walked only parent-child edges,
// so children that exist purely by dotted-ID convention (classic ParentID
// fallback, issueops/filters.go) were dropped from --tree --parent under the
// proxied stack.
func (s *testSuite) TestGetDescendantsDottedOrphans() {
	r := s.issueRepo()
	deps := s.depRepo()

	for _, id := range []string{
		"bd-tree-r",     // root
		"bd-tree-c",     // edge child of root
		"bd-tree-c.7",   // dotted orphan under the edge child (no dep rows)
		"bd-tree-r.1",   // dotted orphan under the root (no dep rows)
		"bd-tree-r.1.2", // nested dotted orphan (no dep rows)
		"bd-tree-m",     // edge child of the dotted orphan bd-tree-r.1
		"bd-tree-z",     // unrelated root
		"bd-tree-r.9",   // dotted ID but re-parented by edge to bd-tree-z
	} {
		s.Require().NoError(r.Insert(s.Ctx(), newTestIssue(id, "tree "+id), "tester", domain.InsertIssueOpts{}))
	}

	for _, e := range []struct{ child, parent string }{
		{"bd-tree-c", "bd-tree-r"},
		{"bd-tree-m", "bd-tree-r.1"},
		{"bd-tree-r.9", "bd-tree-z"},
	} {
		s.Require().NoError(deps.Insert(s.Ctx(),
			&types.Dependency{IssueID: e.child, DependsOnID: e.parent, Type: types.DepParentChild}, "tester", domain.DepInsertOpts{}))
	}

	// Wisps participate in the same walk: an edge wisp child plus a dotted
	// wisp orphan, with their edges in wisp_dependencies. A non-empty wisps
	// table also flips walkWisps on, exercising the wisp CTE branches.
	for _, id := range []string{"bd-tree-wc", "bd-tree-r.5"} {
		s.Require().NoError(r.Insert(s.Ctx(), newTestIssue(id, "wisp "+id), "tester",
			domain.InsertIssueOpts{UseWispsTable: true}))
	}
	s.Require().NoError(deps.Insert(s.Ctx(),
		&types.Dependency{IssueID: "bd-tree-wc", DependsOnID: "bd-tree-r", Type: types.DepParentChild},
		"tester", domain.DepInsertOpts{UseWispsTable: true}))

	got, err := r.GetDescendants(s.Ctx(), "bd-tree-r", types.IssueFilter{})
	s.Require().NoError(err)

	ids := make([]string, len(got))
	for i, issue := range got {
		ids[i] = issue.ID
	}
	s.ElementsMatch([]string{
		"bd-tree-c",     // edge child
		"bd-tree-c.7",   // dotted orphan under edge child
		"bd-tree-r.1",   // dotted orphan under root
		"bd-tree-r.1.2", // nested dotted orphan
		"bd-tree-m",     // edge child hanging off a dotted orphan
		"bd-tree-wc",    // edge wisp child
		"bd-tree-r.5",   // dotted wisp orphan
	}, ids, "dotted-ID orphans must be walked like classic's ParentID fallback; "+
		"bd-tree-r.9 has a parent-child edge elsewhere and must stay out")

	skip := types.IssueFilter{SkipWisps: true}
	got, err = r.GetDescendants(s.Ctx(), "bd-tree-r", skip)
	s.Require().NoError(err)
	ids = ids[:0]
	for _, issue := range got {
		ids = append(ids, issue.ID)
	}
	s.ElementsMatch([]string{"bd-tree-c", "bd-tree-c.7", "bd-tree-r.1", "bd-tree-r.1.2", "bd-tree-m"},
		ids, "SkipWisps must drop the wisp rows but keep the dotted-ID issue walk")
}
