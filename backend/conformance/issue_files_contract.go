package conformance

import (
	"context"
	"errors"
	"testing"

	"github.com/steveyegge/beads/codemapops"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// This file holds the contract every implementation of codemapops.IssueFiles
// must satisfy. Each case asserts what codemapops/issue_files.go PROMISES
// rather than what any one backend happens to do today; a backend that
// disagrees is parked at its own wiring site with a KNOWN DIVERGENCE skip so
// the case still runs on the ones that agree.
//
// THE VOTE COUNT: all three legs share the …InTx bodies in
// internal/storage/codemapops, reached by the stores directly and by the unit
// of work through domain.CodeMapUseCase — ONE reading plus an engine check;
// assert sentinels and typed-error fields, not message text.
//
// THIS IS THE ONE PER-ISSUE TABLE IN THE PLANE, and the cases are shaped by
// that. codemapops/doc.go says issue_files "cascades with the issue like
// provenance_events", so DeletingIssueCascades reads the RAW ROWS after a
// delete rather than asking the role — a role that answered an empty list over
// a table full of orphans would pass every other case in this file.
//
// EVERY CASE OWNS ITS OWN ISSUE IDS AND REPO ID, namespaced under the fixture
// prefix, because the issues plane and the code-map plane are both global to
// the database the three wirings each share across a whole suite.
//
// THE ISSUES THESE CASES SEED ARE DURABLE, never ephemeral: an ephemeral id is
// routed to the wisps plane by the store backends' own create, and issue_files'
// foreign key names the issues table.

// IssueFilesFixture supplies adapter-specific storage access for the IssueFiles
// assertions. Every field is named and typed exactly like the per-backend
// roleFixtureKit hook it is filled from.
type IssueFilesFixture struct {
	// IssuePrefix namespaces the issue ids, repo ids and paths each assertion
	// writes, so several of them can share one database.
	IssuePrefix string
	IssueFiles  codemapops.IssueFiles
	// Indexer builds the code map the join case needs. It is a SEED hook here,
	// not the subject: its own promises are pinned by the Indexer contract.
	Indexer codemapops.Indexer
	// CreateIssue seeds a durable issue in the issues plane, which is what the
	// issue_files foreign key points at.
	CreateIssue func(context.Context, *types.Issue, string) error
	// DeleteIssue removes an issue OUT OF BAND, past this role, because the
	// cascade clause is a promise about what happens to issue_files when
	// something else deletes an issue. The role fixture kit has no delete seam,
	// so each wiring builds this closure over its own backend's delete.
	DeleteIssue func(context.Context, string) error
	// QueryScalar runs a single-row query and scans it, and RETURNS the error
	// rather than failing the test. It is how the cases read RAW ROWS — the
	// only way to tell "the answer looks right" from "the table is right".
	QueryScalar func(context.Context, string, []any, ...any) error
}

// codeMapCommitSHA is a well-formed 40-hex commit sha; RecordInTx refuses
// anything else.
const codeMapCommitSHA = "0123456789012345678901234567890123456789"

// seedIssueFilesIssue creates one durable issue for a case and fails the test
// if the seed does not land.
func seedIssueFilesIssue(t *testing.T, ctx context.Context, fixture IssueFilesFixture, id string, status types.Status) {
	t.Helper()
	if err := fixture.CreateIssue(ctx, &types.Issue{
		ID: id, Title: id, Status: status, Priority: 2, IssueType: types.TypeTask,
	}, "seed"); err != nil {
		t.Fatalf("seeding issue %s: %v", id, err)
	}
}

// countIssueFilesRows reads how many issue_files rows an issue has, as a RAW
// ROW.
func countIssueFilesRows(t *testing.T, ctx context.Context, fixture IssueFilesFixture, issueID string) int {
	t.Helper()
	var n int
	if err := fixture.QueryScalar(ctx, "SELECT COUNT(*) FROM issue_files WHERE issue_id = ?", []any{issueID}, &n); err != nil {
		t.Fatalf("counting issue_files rows for %s: %v", issueID, err)
	}
	return n
}

// RunIssueFilesRecordRefusesUnknownIssue pins RecordRequest in
// codemapops/issue_files.go: it "asks IssueFiles to note that an ISSUE touched
// some paths". No issue, nothing to note — and the refusal is
// storage.ErrNotFound, the plane's miss, not a validation failure.
func RunIssueFilesRecordRefusesUnknownIssue(t *testing.T, ctx context.Context, fixture IssueFilesFixture) {
	p := codeMapPathsFor(fixture.IssuePrefix, "if-unknown")
	missing := fixture.IssuePrefix + "-if-nobody"

	_, err := fixture.IssueFiles.Record(ctx, codemapops.RecordRequest{
		IssueID: missing, RepoID: p.repoID, Paths: []string{p.fileA}, Source: codemapops.SourceHook})
	if !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("Record for an unknown issue = %v, want storage.ErrNotFound", err)
	}
	if n := countIssueFilesRows(t, ctx, fixture, missing); n != 0 {
		t.Errorf("a refused Record wrote %d issue_files rows, want 0", n)
	}
}

// RunIssueFilesRecordRefusesEscapingPath pins the repo-relative promise the
// plane is built on — codemapops/doc.go calls the map "per-REPOSITORY" and
// Node.Path "repo-relative, forward slashes". A path climbing out of the
// repository is codemapops.ErrValidation.
func RunIssueFilesRecordRefusesEscapingPath(t *testing.T, ctx context.Context, fixture IssueFilesFixture) {
	p := codeMapPathsFor(fixture.IssuePrefix, "if-escape")
	issue := fixture.IssuePrefix + "-if-escape-1"
	seedIssueFilesIssue(t, ctx, fixture, issue, types.StatusOpen)

	for _, bad := range []string{"../outside.go", "/etc/passwd", ""} {
		_, err := fixture.IssueFiles.Record(ctx, codemapops.RecordRequest{
			IssueID: issue, RepoID: p.repoID, Paths: []string{bad}, Source: codemapops.SourceHook})
		if !errors.Is(err, codemapops.ErrValidation) {
			t.Errorf("Record of %q = %v, want codemapops.ErrValidation", bad, err)
		}
	}
	if n := countIssueFilesRows(t, ctx, fixture, issue); n != 0 {
		t.Errorf("refused paths wrote %d issue_files rows, want 0", n)
	}
}

// RunIssueFilesRecordRefusesBadSHA pins RecordRequest.CommitSHA in
// codemapops/issue_files.go: the evidence a commit-sourced sighting carries has
// to be a real commit id, so a short one is codemapops.ErrValidation rather
// than a stored half-truth.
func RunIssueFilesRecordRefusesBadSHA(t *testing.T, ctx context.Context, fixture IssueFilesFixture) {
	p := codeMapPathsFor(fixture.IssuePrefix, "if-badsha")
	issue := fixture.IssuePrefix + "-if-badsha-1"
	seedIssueFilesIssue(t, ctx, fixture, issue, types.StatusOpen)

	_, err := fixture.IssueFiles.Record(ctx, codemapops.RecordRequest{
		IssueID: issue, RepoID: p.repoID, Paths: []string{p.fileA}, Source: codemapops.SourceCommit, CommitSHA: "abc"})
	if !errors.Is(err, codemapops.ErrValidation) {
		t.Fatalf("Record with a short commit sha = %v, want codemapops.ErrValidation", err)
	}
	if n := countIssueFilesRows(t, ctx, fixture, issue); n != 0 {
		t.Errorf("a refused Record wrote %d issue_files rows, want 0", n)
	}
}

// RunIssueFilesRecordUpgradesEvidence pins Source in
// codemapops/issue_files.go — it "classifies how a file-to-issue association
// was OBSERVED" — together with RecordResult's inserted-versus-updated split
// and IssueFile.Touches. A repeat sighting folds into the existing row, a
// stronger source replaces a weaker one, and a WEAKER one never demotes the
// row it lands on.
func RunIssueFilesRecordUpgradesEvidence(t *testing.T, ctx context.Context, fixture IssueFilesFixture) {
	p := codeMapPathsFor(fixture.IssuePrefix, "if-evidence")
	issue := fixture.IssuePrefix + "-if-evidence-1"
	seedIssueFilesIssue(t, ctx, fixture, issue, types.StatusOpen)

	first, err := fixture.IssueFiles.Record(ctx, codemapops.RecordRequest{
		IssueID: issue, RepoID: p.repoID, Paths: []string{p.fileA}, Source: codemapops.SourceHook})
	if err != nil || first.Inserted != 1 || first.Updated != 0 {
		t.Fatalf("first sighting = %+v, %v; want 1 inserted", first, err)
	}
	second, err := fixture.IssueFiles.Record(ctx, codemapops.RecordRequest{
		IssueID: issue, RepoID: p.repoID, Paths: []string{p.fileA}, Source: codemapops.SourceCommit, CommitSHA: codeMapCommitSHA})
	if err != nil || second.Updated != 1 || second.Inserted != 0 {
		t.Fatalf("second sighting of the same path = %+v, %v; want 1 updated", second, err)
	}

	files, err := fixture.IssueFiles.ByIssue(ctx, issue)
	if err != nil {
		t.Fatalf("ByIssue(%s): %v", issue, err)
	}
	if len(files) != 1 {
		t.Fatalf("ByIssue = %+v, want the one folded row", files)
	}
	if files[0].Source != codemapops.SourceCommit {
		t.Errorf("Source = %q after a commit sighting upgraded a hook one, want %q", files[0].Source, codemapops.SourceCommit)
	}
	if files[0].CommitSHA != codeMapCommitSHA {
		t.Errorf("CommitSHA = %q, want the sha the upgrading sighting carried", files[0].CommitSHA)
	}
	if files[0].Touches != 2 {
		t.Errorf("Touches = %d, want 2: a repeat sighting folds in rather than starting a row", files[0].Touches)
	}

	// The point of the ranking: a weaker later sighting keeps the stronger
	// evidence already on the row.
	if _, err := fixture.IssueFiles.Record(ctx, codemapops.RecordRequest{
		IssueID: issue, RepoID: p.repoID, Paths: []string{p.fileA}, Source: codemapops.SourceBranch}); err != nil {
		t.Fatalf("third sighting: %v", err)
	}
	files, err = fixture.IssueFiles.ByIssue(ctx, issue)
	if err != nil {
		t.Fatalf("ByIssue after the weaker sighting: %v", err)
	}
	if len(files) != 1 || files[0].Source != codemapops.SourceCommit || files[0].CommitSHA != codeMapCommitSHA {
		t.Errorf("ByIssue = %+v; a branch sighting must not demote commit evidence", files)
	}
	if files[0].Touches != 3 {
		t.Errorf("Touches = %d, want 3: a refused DOWNGRADE is still a sighting", files[0].Touches)
	}
}

// RunIssueFilesByPathOpenOnlyFiltersClosed pins ByPath's openOnly parameter in
// codemapops/issue_files.go against IssueRef, "a lightweight reference to
// another issue, for cross-linking": the filter is about the issue's status,
// not about which rows exist.
func RunIssueFilesByPathOpenOnlyFiltersClosed(t *testing.T, ctx context.Context, fixture IssueFilesFixture) {
	p := codeMapPathsFor(fixture.IssuePrefix, "if-bypath")
	open := fixture.IssuePrefix + "-if-bypath-open"
	closed := fixture.IssuePrefix + "-if-bypath-closed"
	seedIssueFilesIssue(t, ctx, fixture, open, types.StatusOpen)
	seedIssueFilesIssue(t, ctx, fixture, closed, types.StatusClosed)
	// The seed is read back as a RAW ROW before the filter is asked anything: a
	// backend that normalized the created status to open would otherwise fail
	// this case at the filter, where the message would blame ByPath for a
	// seeding problem.
	var seededStatus string
	if err := fixture.QueryScalar(ctx, "SELECT status FROM issues WHERE id = ?", []any{closed}, &seededStatus); err != nil {
		t.Fatalf("reading the seeded status of %s: %v", closed, err)
	}
	if seededStatus != string(types.StatusClosed) {
		t.Fatalf("seeded status of %s = %q, want %q: this case needs a genuinely closed issue",
			closed, seededStatus, types.StatusClosed)
	}
	for _, id := range []string{open, closed} {
		if _, err := fixture.IssueFiles.Record(ctx, codemapops.RecordRequest{
			IssueID: id, RepoID: p.repoID, Paths: []string{p.fileA}, Source: codemapops.SourceHook}); err != nil {
			t.Fatalf("recording %s: %v", id, err)
		}
	}

	all, err := fixture.IssueFiles.ByPath(ctx, p.repoID, p.fileA, false)
	if err != nil {
		t.Fatalf("ByPath(openOnly=false): %v", err)
	}
	if len(all) != 2 {
		t.Errorf("ByPath(openOnly=false) = %+v, want both issues", all)
	}
	onlyOpen, err := fixture.IssueFiles.ByPath(ctx, p.repoID, p.fileA, true)
	if err != nil {
		t.Fatalf("ByPath(openOnly=true): %v", err)
	}
	if len(onlyOpen) != 1 || onlyOpen[0].ID != open {
		t.Fatalf("ByPath(openOnly=true) = %+v, want just %s", onlyOpen, open)
	}
	if onlyOpen[0].Title != open {
		t.Errorf("IssueRef.Title = %q, want %q: the ref carries what a cross-link displays", onlyOpen[0].Title, open)
	}
}

// RunIssueFilesIssueCodeContextWithoutMapHasNilContext pins IssueCodeFile's
// field doc in codemapops/issue_files.go: "Context *FileContext // nil when the
// repo is not indexed or the path is unknown", and IssueCodeContext's "Shape
// *Shape // freshness header; nil when not indexed". The touched files are
// still answered — an unindexed repository loses the code context, not the
// history.
func RunIssueFilesIssueCodeContextWithoutMapHasNilContext(t *testing.T, ctx context.Context, fixture IssueFilesFixture) {
	// Its OWN repo id, deliberately never indexed by any other case.
	p := codeMapPathsFor(fixture.IssuePrefix, "if-nomap")
	issue := fixture.IssuePrefix + "-if-nomap-1"
	seedIssueFilesIssue(t, ctx, fixture, issue, types.StatusOpen)
	if _, err := fixture.IssueFiles.Record(ctx, codemapops.RecordRequest{
		IssueID: issue, RepoID: p.repoID, Paths: []string{p.fileA}, Source: codemapops.SourceHook}); err != nil {
		t.Fatalf("recording %s: %v", issue, err)
	}

	cc, err := fixture.IssueFiles.IssueCodeContext(ctx, issue, p.repoID)
	if err != nil {
		t.Fatalf("IssueCodeContext: %v", err)
	}
	if cc.Indexed {
		t.Fatalf("Indexed = true for a repository with no code map")
	}
	if cc.Shape != nil {
		t.Errorf("Shape = %+v, want nil when not indexed", cc.Shape)
	}
	if len(cc.Files) != 1 || cc.Files[0].Path != p.fileA {
		t.Fatalf("Files = %+v, want the one touched path: the history survives an unindexed repository", cc.Files)
	}
	if cc.Files[0].Context != nil {
		t.Errorf("Context = %+v, want nil when the repo is not indexed", cc.Files[0].Context)
	}
}

// RunIssueFilesIssueCodeContextWithMapJoins pins the other half of
// IssueCodeContext in codemapops/issue_files.go — "the full 'what code does
// this issue touch' answer" — once the repository IS indexed: the shape header
// arrives, each touched path carries its file context, and Siblings names the
// OTHER open issues on that path.
func RunIssueFilesIssueCodeContextWithMapJoins(t *testing.T, ctx context.Context, fixture IssueFilesFixture) {
	p := codeMapPathsFor(fixture.IssuePrefix, "if-map")
	issue := fixture.IssuePrefix + "-if-map-1"
	sibling := fixture.IssuePrefix + "-if-map-2"
	seedIssueFilesIssue(t, ctx, fixture, issue, types.StatusOpen)
	seedIssueFilesIssue(t, ctx, fixture, sibling, types.StatusOpen)
	if _, err := fixture.Indexer.Apply(ctx, codemapops.ApplyRequest{
		RepoID: p.repoID, HeadSHA: "abc", Graph: codeMapTwoPkgGraph(p)}); err != nil {
		t.Fatalf("seeding the code map: %v", err)
	}
	for _, id := range []string{issue, sibling} {
		if _, err := fixture.IssueFiles.Record(ctx, codemapops.RecordRequest{
			IssueID: id, RepoID: p.repoID, Paths: []string{p.fileA}, Source: codemapops.SourceHook}); err != nil {
			t.Fatalf("recording %s: %v", id, err)
		}
	}

	cc, err := fixture.IssueFiles.IssueCodeContext(ctx, issue, p.repoID)
	if err != nil {
		t.Fatalf("IssueCodeContext: %v", err)
	}
	if !cc.Indexed || cc.Shape == nil {
		t.Fatalf("Indexed = %v with Shape %+v, want an indexed repository carrying its freshness header", cc.Indexed, cc.Shape)
	}
	if cc.IssueID != issue || cc.RepoID != p.repoID {
		t.Errorf("IssueCodeContext identity = %q/%q, want %q/%q", cc.IssueID, cc.RepoID, issue, p.repoID)
	}
	if len(cc.Files) != 1 {
		t.Fatalf("Files = %+v, want the one touched path", cc.Files)
	}
	if cc.Files[0].Context == nil || cc.Files[0].Context.Package.Path != p.pkgA {
		t.Fatalf("Context = %+v, want the file joined to package %s", cc.Files[0].Context, p.pkgA)
	}
	if len(cc.Files[0].Siblings) != 1 || cc.Files[0].Siblings[0].ID != sibling {
		t.Errorf("Siblings = %+v, want just %s: the issue itself is not its own sibling", cc.Files[0].Siblings, sibling)
	}
}

// RunIssueFilesDeletingIssueCascades pins codemapops/doc.go's clause that
// issue_files "is the one per-issue table in the plane, and it cascades with
// the issue like provenance_events".
//
// The delete is OUT OF BAND, past this role, because that is the whole claim: a
// deletion nothing in this plane knows about still takes the plane's rows with
// it. And the reading is a RAW COUNT rather than ByIssue, because a role that
// answered an empty list over a table full of orphaned rows would pass every
// other case in this file.
func RunIssueFilesDeletingIssueCascades(t *testing.T, ctx context.Context, fixture IssueFilesFixture) {
	p := codeMapPathsFor(fixture.IssuePrefix, "if-cascade")
	doomed := fixture.IssuePrefix + "-if-cascade-1"
	survivor := fixture.IssuePrefix + "-if-cascade-2"
	seedIssueFilesIssue(t, ctx, fixture, doomed, types.StatusOpen)
	seedIssueFilesIssue(t, ctx, fixture, survivor, types.StatusOpen)
	for _, id := range []string{doomed, survivor} {
		if _, err := fixture.IssueFiles.Record(ctx, codemapops.RecordRequest{
			IssueID: id, RepoID: p.repoID, Paths: []string{p.fileA, p.fileB}, Source: codemapops.SourceHook}); err != nil {
			t.Fatalf("recording %s: %v", id, err)
		}
	}
	if n := countIssueFilesRows(t, ctx, fixture, doomed); n != 2 {
		t.Fatalf("issue_files rows before the delete = %d, want 2", n)
	}

	if err := fixture.DeleteIssue(ctx, doomed); err != nil {
		t.Fatalf("deleting %s out of band: %v", doomed, err)
	}
	if n := countIssueFilesRows(t, ctx, fixture, doomed); n != 0 {
		t.Errorf("issue_files rows after deleting the issue = %d, want 0: the plane cascades with the issue", n)
	}
	if n := countIssueFilesRows(t, ctx, fixture, survivor); n != 2 {
		t.Errorf("the surviving issue's rows = %d, want its own 2: a cascade is not a sweep", n)
	}
}
