package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/steveyegge/beads/codemapops"
	"github.com/steveyegge/beads/internal/storage"
)

// The pins for the code-map operations. What is asserted here is the WIRE EDGE
// — that each handler decodes its parameters into the role's call faithfully,
// refuses what the document refuses, and maps the role's three failure kinds
// onto the three statuses the document publishes. What a code map MEANS (path
// normalization, which edges exist, what makes a summary stale) is the role's
// conformance contract and is deliberately not re-asserted here.

const (
	codemapFilesPath = "/v0/beads/codemap/files/"
	codemapShapePath = "/v0/beads/codemap/shape"
	testRepoID       = "repo-abc"
)

// roleCodeMapReader is the code map's read side for a store-shaped source.
type roleCodeMapReader struct {
	file  codemapops.FileContext
	shape codemapops.Shape
	err   error

	mu         sync.Mutex
	fileCalls  []string // "repoID\x00path"
	shapeCalls []codemapops.ShapeOptions
}

func (r *roleCodeMapReader) FileContext(_ context.Context, repoID, path string) (codemapops.FileContext, error) {
	r.mu.Lock()
	r.fileCalls = append(r.fileCalls, repoID+"\x00"+path)
	r.mu.Unlock()
	if r.err != nil {
		return codemapops.FileContext{}, r.err
	}
	return r.file, nil
}

func (r *roleCodeMapReader) PackageContext(context.Context, string, string) (codemapops.PackageContext, error) {
	return codemapops.PackageContext{}, nil
}

func (r *roleCodeMapReader) Stale(context.Context, string, int) ([]codemapops.NodeRef, error) {
	return nil, nil
}

func (r *roleCodeMapReader) Shape(_ context.Context, _ string, opts codemapops.ShapeOptions) (codemapops.Shape, error) {
	r.mu.Lock()
	r.shapeCalls = append(r.shapeCalls, opts)
	r.mu.Unlock()
	if r.err != nil {
		return codemapops.Shape{}, r.err
	}
	return r.shape, nil
}

func (r *roleCodeMapReader) files() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.fileCalls...)
}

func (r *roleCodeMapReader) shapes() []codemapops.ShapeOptions {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]codemapops.ShapeOptions(nil), r.shapeCalls...)
}

// roleIssueFiles is the issue-to-path plane for a store-shaped source.
type roleIssueFiles struct {
	recorded codemapops.RecordResult
	byIssue  []codemapops.IssueFile
	byPath   []codemapops.IssueRef
	err      error

	mu          sync.Mutex
	records     []codemapops.RecordRequest
	byPathCalls []string // "repoID\x00path\x00openOnly"
}

func (f *roleIssueFiles) Record(_ context.Context, req codemapops.RecordRequest) (codemapops.RecordResult, error) {
	f.mu.Lock()
	f.records = append(f.records, req)
	f.mu.Unlock()
	if f.err != nil {
		return codemapops.RecordResult{}, f.err
	}
	return f.recorded, nil
}

func (f *roleIssueFiles) ByIssue(context.Context, string) ([]codemapops.IssueFile, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.byIssue, nil
}

func (f *roleIssueFiles) ByPath(_ context.Context, repoID, path string, openOnly bool) ([]codemapops.IssueRef, error) {
	f.mu.Lock()
	f.byPathCalls = append(f.byPathCalls, fmt.Sprintf("%s\x00%s\x00%t", repoID, path, openOnly))
	f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	return f.byPath, nil
}

func (f *roleIssueFiles) IssueCodeContext(context.Context, string, string) (codemapops.IssueCodeContext, error) {
	return codemapops.IssueCodeContext{}, nil
}

func (f *roleIssueFiles) recordRequests() []codemapops.RecordRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]codemapops.RecordRequest(nil), f.records...)
}

func (f *roleIssueFiles) pathCalls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.byPathCalls...)
}

// TestGetCodemapFileForwardsThePathAndAsksForOpenIssues is this operation's
// central pin: the encoded path is decoded ONCE and reaches both roles
// verbatim, and the issue lookup is the OPEN one.
func TestGetCodemapFileForwardsThePathAndAsksForOpenIssues(t *testing.T) {
	reader := &roleCodeMapReader{file: codemapops.FileContext{
		Node:    codemapops.NodeRef{Path: "cmd/bd/codemap.go", Kind: codemapops.NodeFile, Lang: "go", Summary: "the code map's verbs"},
		Package: codemapops.NodeRef{Path: "github.com/x/cmd/bd", Kind: codemapops.NodePackage},
		Imports: []codemapops.NodeRef{{Path: "github.com/x/codemapops", Kind: codemapops.NodePackage}},
	}}
	files := &roleIssueFiles{byPath: []codemapops.IssueRef{{ID: "bd-1", Title: "fix it", Status: "open"}}}
	ts := newTestServer(t, rolesConfig(Config{CodeMapReader: reader, IssueFiles: files}))

	resp := ts.get(t, codemapFilesPath+"cmd%2Fbd%2Fcodemap.go?repo_id="+testRepoID)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", resp.StatusCode, readAll(t, resp))
	}

	if got := reader.files(); len(got) != 1 || got[0] != testRepoID+"\x00cmd/bd/codemap.go" {
		t.Errorf("reader received %q, want the repo id and the decoded path", got)
	}
	if got := files.pathCalls(); len(got) != 1 || got[0] != testRepoID+"\x00cmd/bd/codemap.go\x00true" {
		t.Errorf("issue-files received %q, want the same path with openOnly true", got)
	}

	body := decodeBody(t, resp)
	file, ok := body["file"].(map[string]any)
	if !ok {
		t.Fatalf("no `file` object in %v", body)
	}
	node, ok := file["node"].(map[string]any)
	if !ok {
		t.Fatalf("no `node` in %v", file)
	}
	if node["path"] != "cmd/bd/codemap.go" || node["kind"] != "file" {
		t.Errorf("node = %v, want the role's file node under snake_case names", node)
	}
	issues, ok := body["issues"].([]any)
	if !ok || len(issues) != 1 {
		t.Fatalf("issues = %v, want the one open issue", body["issues"])
	}
}

// TestGetCodemapFileAnswersAnEmptyIssueListAsAnArray: nobody working on a file
// is an empty array, never null, so a client reads the answer rather than the
// absence of a key.
func TestGetCodemapFileAnswersAnEmptyIssueListAsAnArray(t *testing.T) {
	ts := newTestServer(t, rolesConfig(Config{
		CodeMapReader: &roleCodeMapReader{},
		IssueFiles:    &roleIssueFiles{},
	}))

	resp := ts.get(t, codemapFilesPath+"a%2Fa.go?repo_id="+testRepoID)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", resp.StatusCode, readAll(t, resp))
	}
	issues, ok := decodeBody(t, resp)["issues"].([]any)
	if !ok || len(issues) != 0 {
		t.Errorf("issues = %v, want an empty array", issues)
	}
}

// TestCodemapReadsRefuseAMissingRepoID pins the one parameter this surface
// requires rather than infers.
func TestCodemapReadsRefuseAMissingRepoID(t *testing.T) {
	for _, path := range []string{codemapFilesPath + "a%2Fa.go", codemapShapePath} {
		t.Run(path, func(t *testing.T) {
			ts := newTestServer(t, rolesConfig(Config{
				CodeMapReader: &roleCodeMapReader{},
				IssueFiles:    &roleIssueFiles{},
			}))
			resp := ts.get(t, path)
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400: %s", resp.StatusCode, readAll(t, resp))
			}
			body := decodeBody(t, resp)
			if body["code"] != string(CodeInvalidArgument) || body["param"] != "repo_id" {
				t.Errorf("problem = %v, want invalid_argument naming repo_id", body)
			}
		})
	}
}

// TestCodemapReadsMapTheRolesThreeFailures is the error table: an unindexed
// repository is the 409 with the code that names the fix, a path the map does
// not hold is a 404, and a refusal from the role's validator is a 400.
func TestCodemapReadsMapTheRolesThreeFailures(t *testing.T) {
	for _, tc := range []struct {
		name   string
		err    error
		status int
		code   Code
	}{
		{"not indexed", &codemapops.ErrNotIndexed{RepoID: testRepoID}, http.StatusConflict, CodeCodeMapNotBuilt},
		{"unknown path", fmt.Errorf("%w: file %q", storage.ErrNotFound, "a/a.go"), http.StatusNotFound, CodeNotFound},
		{"refused path", fmt.Errorf("%w: path escapes the repository", codemapops.ErrValidation), http.StatusBadRequest, CodeInvalidArgument},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ts := newTestServer(t, rolesConfig(Config{
				CodeMapReader: &roleCodeMapReader{err: tc.err},
				IssueFiles:    &roleIssueFiles{},
			}))
			resp := ts.get(t, codemapFilesPath+"a%2Fa.go?repo_id="+testRepoID)
			if resp.StatusCode != tc.status {
				t.Fatalf("status = %d, want %d: %s", resp.StatusCode, tc.status, readAll(t, resp))
			}
			if got := decodeBody(t, resp)["code"]; got != string(tc.code) {
				t.Errorf("code = %v, want %q", got, tc.code)
			}
		})
	}
}

// TestGetCodemapShapeForwardsTopFiles: an absent parameter leaves the role's
// own default, and a supplied one reaches it unchanged.
func TestGetCodemapShapeForwardsTopFiles(t *testing.T) {
	indexed := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	reader := &roleCodeMapReader{shape: codemapops.Shape{
		RepoID: testRepoID, HeadSHA: "abc", IndexedAt: indexed, Nodes: 12, Edges: 20,
		Layers: []codemapops.LayerSummary{{Layer: "cli", Packages: 1, Files: 3}},
	}}
	ts := newTestServer(t, rolesConfig(Config{CodeMapReader: reader, IssueFiles: &roleIssueFiles{}}))

	resp := ts.get(t, codemapShapePath+"?repo_id="+testRepoID)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", resp.StatusCode, readAll(t, resp))
	}
	body := decodeBody(t, resp)
	if body["repo_id"] != testRepoID || body["head_sha"] != "abc" || body["nodes"] != float64(12) {
		t.Errorf("shape = %v, want the role's answer under snake_case names", body)
	}

	if resp := ts.get(t, codemapShapePath+"?repo_id="+testRepoID+"&top_files=3"); resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", resp.StatusCode, readAll(t, resp))
	}
	calls := reader.shapes()
	if len(calls) != 2 {
		t.Fatalf("%d shape calls, want 2", len(calls))
	}
	if calls[0].TopFiles != 0 {
		t.Errorf("absent top_files reached the role as %d, want the role's default (0)", calls[0].TopFiles)
	}
	if calls[1].TopFiles != 3 {
		t.Errorf("top_files reached the role as %d, want 3", calls[1].TopFiles)
	}

	if resp := ts.get(t, codemapShapePath+"?repo_id="+testRepoID+"&top_files=-1"); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("negative top_files: status = %d, want 400", resp.StatusCode)
	}
}

// TestListIssueFilesAnswersTheRolesRowsAsAPage, and an issue with nothing
// recorded is an empty page rather than a miss.
func TestListIssueFilesAnswersTheRolesRowsAsAPage(t *testing.T) {
	seen := time.Date(2026, 9, 5, 9, 0, 0, 0, time.UTC)
	files := &roleIssueFiles{byIssue: []codemapops.IssueFile{
		{Path: "a/a.go", Source: codemapops.SourceManual, FirstSeen: seen, LastSeen: seen, Touches: 2},
	}}
	ts := newTestServer(t, rolesConfig(Config{CodeMapReader: &roleCodeMapReader{}, IssueFiles: files}))

	resp := ts.get(t, "/v0/beads/issues/bd-1/files")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", resp.StatusCode, readAll(t, resp))
	}
	body := decodeBody(t, resp)
	if body["has_more"] != false {
		t.Errorf("has_more = %v, want false", body["has_more"])
	}
	items, ok := body["items"].([]any)
	if !ok || len(items) != 1 {
		t.Fatalf("items = %v, want the role's one row", body["items"])
	}
	row, _ := items[0].(map[string]any)
	if row["path"] != "a/a.go" || row["source"] != "manual" || row["touches"] != float64(2) {
		t.Errorf("row = %v, want the role's row under snake_case names", row)
	}

	empty := newTestServer(t, rolesConfig(Config{CodeMapReader: &roleCodeMapReader{}, IssueFiles: &roleIssueFiles{}}))
	resp = empty.get(t, "/v0/beads/issues/bd-nothing/files")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", resp.StatusCode, readAll(t, resp))
	}
	if items, ok := decodeBody(t, resp)["items"].([]any); !ok || len(items) != 0 {
		t.Errorf("items = %v, want an empty array", items)
	}
}

// TestRecordIssueFilesForwardsTheBody: the id comes from the path, the repo
// from the query, the paths from the body VERBATIM — no normalization here —
// and the source is the one this surface writes.
func TestRecordIssueFilesForwardsTheBody(t *testing.T) {
	files := &roleIssueFiles{recorded: codemapops.RecordResult{Inserted: 1, Updated: 1}}
	ts := newTestServer(t, rolesConfig(Config{CodeMapReader: &roleCodeMapReader{}, IssueFiles: files}))

	resp := ts.postBody(t, "/v0/beads/issues/bd-1/files?repo_id="+testRepoID, "application/json",
		`{"paths":["a/a.go","./b/b.go"],"source":"manual"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", resp.StatusCode, readAll(t, resp))
	}

	reqs := files.recordRequests()
	if len(reqs) != 1 {
		t.Fatalf("%d records, want 1", len(reqs))
	}
	got := reqs[0]
	if got.IssueID != "bd-1" || got.RepoID != testRepoID || got.Source != codemapops.SourceManual {
		t.Errorf("role received %+v, want the path's id, the query's repo and source manual", got)
	}
	if len(got.Paths) != 2 || got.Paths[0] != "a/a.go" || got.Paths[1] != "./b/b.go" {
		t.Errorf("paths = %v, want the body's paths verbatim: normalization is the role's", got.Paths)
	}

	body := decodeBody(t, resp)
	if body["inserted"] != float64(1) || body["updated"] != float64(1) {
		t.Errorf("result = %v, want the role's counts", body)
	}
}

// TestRecordIssueFilesRefusesWhatTheDocumentRefuses covers the body vocabulary
// and the source narrowing, which is this operation's own rule: the three
// observed sources are not assertable over the wire.
func TestRecordIssueFilesRefusesWhatTheDocumentRefuses(t *testing.T) {
	for _, tc := range []struct {
		name, body, param string
		reason            Reason
	}{
		{"unknown member", `{"paths":["a"],"source":"manual","commit_sha":"x"}`, "commit_sha", ReasonUnknownParameter},
		{"no paths", `{"source":"manual"}`, "paths", ReasonInvalidValue},
		{"null paths", `{"paths":null,"source":"manual"}`, "paths", ReasonInvalidValue},
		{"paths not an array", `{"paths":"a/a.go","source":"manual"}`, "paths", ReasonInvalidValue},
		{"no source", `{"paths":["a/a.go"]}`, "source", ReasonInvalidValue},
		{"observed source", `{"paths":["a/a.go"],"source":"commit"}`, "source", ReasonInvalidValue},
		{"unknown source", `{"paths":["a/a.go"],"source":"guess"}`, "source", ReasonInvalidValue},
	} {
		t.Run(tc.name, func(t *testing.T) {
			files := &roleIssueFiles{}
			ts := newTestServer(t, rolesConfig(Config{CodeMapReader: &roleCodeMapReader{}, IssueFiles: files}))

			resp := ts.postBody(t, "/v0/beads/issues/bd-1/files?repo_id="+testRepoID, "application/json", tc.body)
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400: %s", resp.StatusCode, readAll(t, resp))
			}
			body := decodeBody(t, resp)
			if body["code"] != string(CodeInvalidArgument) {
				t.Errorf("code = %v, want invalid_argument", body["code"])
			}
			if body["param"] != tc.param || body["reason"] != string(tc.reason) {
				t.Errorf("problem = %v, want param %q reason %q", body, tc.param, tc.reason)
			}
			if got := files.recordRequests(); len(got) != 0 {
				t.Errorf("a refused request reached the role: %+v", got)
			}
		})
	}
}

// TestRecordIssueFilesAnswers404ForAnIssueThatIsNotThere: unlike the read
// beside it, this operation probes the id — through the role, which is where
// the probe lives.
func TestRecordIssueFilesAnswers404ForAnIssueThatIsNotThere(t *testing.T) {
	files := &roleIssueFiles{err: fmt.Errorf("%w: issue bd-404", storage.ErrNotFound)}
	ts := newTestServer(t, rolesConfig(Config{CodeMapReader: &roleCodeMapReader{}, IssueFiles: files}))

	resp := ts.postBody(t, "/v0/beads/issues/bd-404/files?repo_id="+testRepoID, "application/json",
		`{"paths":["a/a.go"],"source":"manual"}`)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404: %s", resp.StatusCode, readAll(t, resp))
	}
	if got := decodeBody(t, resp)["code"]; got != string(CodeNotFound) {
		t.Errorf("code = %v, want not_found", got)
	}
}
