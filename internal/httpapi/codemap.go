package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"

	"github.com/steveyegge/beads/codemapops"
	"github.com/steveyegge/beads/internal/httpapi/apigen"
	"github.com/steveyegge/beads/internal/storage"
)

// The code-map operations. Each one decodes its parameters, hands them to the
// code-map role, and shapes the answer onto the wire.
//
// WHAT IS NOT HERE, as in memories.go: no path is normalized, no escaping path
// is detected, no import edge is walked, no issue id is resolved and no
// "is this repository indexed" probe is performed. All of that is
// codemapops.Reader and codemapops.IssueFiles, which `bd codemap` reaches
// through the same accessors — so a path this surface accepts and one
// `bd codemap show` accepts are the same set by construction rather than by two
// implementations that agree today.
//
// THE REPOSITORY IS NAMED BY THE CALLER on the two map reads, and that is a
// contract decision rather than a missing convenience. One workspace database
// can hold the maps of several repositories; a server that inferred one would
// answer confidently about the wrong tree. The issue-file operations take it
// only where the ROW needs it: a read by issue does not, because an issue lives
// in exactly one workspace.

// codemapRepoIDParam names the required repository selector.
const codemapRepoIDParam = "repo_id"

// codemapTopFilesParam bounds the fan-in list in a shape answer.
const codemapTopFilesParam = "top_files"

// The two 404 sentences this plane can need. They are constants rather than
// literals at the call sites so that the same miss reads the same way whichever
// role reported it.
const (
	codemapPathMissing  = "this repository's code map holds no node for that path"
	codemapIssueMissing = "no issue with that id"
)

// handleGetCodemapFile answers GET /v0/beads/codemap/files/{path}.
func (s *Server) handleGetCodemapFile(w http.ResponseWriter, r *http.Request) {
	q := newQuery(r.URL.Query())
	repoID := q.str(codemapRepoIDParam)
	if !s.acceptQuery(w, r, q) {
		return
	}
	if !s.requireRepoID(w, r, repoID) {
		return
	}
	// ONE percent-decode, performed by ServeMux. The separators arrive encoded
	// because a repo-relative path occupies one segment here, and the decoded
	// value goes to the role verbatim: normalization is the role's, so a
	// spelling this surface accepts is one `bd codemap show` accepts.
	path := r.PathValue("path")
	if strings.TrimSpace(path) == "" {
		s.fail(w, r, InvalidArgument("path", ReasonInvalidValue, "`path` is empty after trimming"))
		return
	}

	reader, err := s.codeMapReader(r)
	if err != nil {
		s.failErr(w, r, err)
		return
	}
	files, err := s.issueFiles(r)
	if err != nil {
		s.failErr(w, r, err)
		return
	}
	context, err := reader.FileContext(r.Context(), repoID, path)
	if err != nil {
		s.failCodemapErr(w, r, err, codemapPathMissing)
		return
	}
	// OPEN issues only. The question this pairing answers is "who is working on
	// this file", and a closed issue answers a different one — the same
	// openOnly `bd codemap show` passes.
	issues, err := files.ByPath(r.Context(), repoID, path, true)
	if err != nil {
		s.failCodemapErr(w, r, err, codemapPathMissing)
		return
	}
	if issues == nil {
		// Always an array, never null: a client reads "nobody is working on
		// this" from the answer rather than from a missing key.
		issues = []codemapops.IssueRef{}
	}
	writeJSON(w, apigen.CodemapFile{File: context, Issues: issues})
}

// handleGetCodemapShape answers GET /v0/beads/codemap/shape.
func (s *Server) handleGetCodemapShape(w http.ResponseWriter, r *http.Request) {
	q := newQuery(r.URL.Query())
	repoID := q.str(codemapRepoIDParam)
	topFiles := q.integer(codemapTopFilesParam)
	if topFiles != nil && *topFiles < 0 {
		q.invalid(codemapTopFilesParam, "`"+codemapTopFilesParam+"` must not be negative")
	}
	if !s.acceptQuery(w, r, q) {
		return
	}
	if !s.requireRepoID(w, r, repoID) {
		return
	}

	reader, err := s.codeMapReader(r)
	if err != nil {
		s.failErr(w, r, err)
		return
	}
	// A zero TopFiles is what the role reads as "your default", so an absent
	// parameter and an explicit 0 are the same request. That is the role's
	// vocabulary, not a translation invented here.
	var opts codemapops.ShapeOptions
	if topFiles != nil {
		opts.TopFiles = *topFiles
	}
	shape, err := reader.Shape(r.Context(), repoID, opts)
	if err != nil {
		// The EMPTY missing sentence, which is this operation's whole 404
		// posture: it addresses a repository rather than a node, an unindexed
		// one is the 409 above, and an empty map is a 200 reporting zero — so
		// there is no miss for it to report and it documents no 404.
		s.failCodemapErr(w, r, err, "")
		return
	}
	writeJSON(w, shape)
}

// handleListIssueFiles answers GET /v0/beads/issues/{id}/files.
func (s *Server) handleListIssueFiles(w http.ResponseWriter, r *http.Request) {
	if !s.requireNoQuery(w, r) {
		return
	}
	id := r.PathValue("id")

	files, err := s.issueFiles(r)
	if err != nil {
		s.failErr(w, r, err)
		return
	}
	rows, err := files.ByIssue(r.Context(), id)
	if err != nil {
		s.failCodemapErr(w, r, err, codemapIssueMissing)
		return
	}
	if rows == nil {
		rows = []codemapops.IssueFile{}
	}
	// has_more is always false: the role answers the whole set, ordered by
	// path, and the envelope is used anyway because that order is stable — which
	// is what would let a cursor be added later without changing this shape.
	writeJSON(w, apigen.IssueFilesPage{Items: rows, HasMore: false})
}

// The request body's member vocabulary. The schema is
// additionalProperties: false, so anything else is refused BY NAME.
const (
	recordPathsMember  = "paths"
	recordSourceMember = "source"
)

var recordMembers = []string{recordPathsMember, recordSourceMember}

// handleRecordIssueFiles answers POST /v0/beads/issues/{id}/files.
func (s *Server) handleRecordIssueFiles(w http.ResponseWriter, r *http.Request) {
	q := newQuery(r.URL.Query())
	repoID := q.str(codemapRepoIDParam)
	if !s.acceptQuery(w, r, q) {
		return
	}
	if !s.requireRepoID(w, r, repoID) {
		return
	}
	if !s.requireJSONContent(w, r) {
		return
	}
	request, ok := s.recordRequest(w, r)
	if !ok {
		return
	}
	request.IssueID = r.PathValue("id")
	request.RepoID = repoID

	files, err := s.issueFiles(r)
	if err != nil {
		s.failErr(w, r, err)
		return
	}
	result, err := files.Record(r.Context(), request)
	if err != nil {
		s.failCodemapErr(w, r, err, codemapIssueMissing)
		return
	}
	writeJSON(w, result)
}

// requireRepoID enforces the one parameter both map reads and the record write
// require. It is a 400 rather than a default because the alternative is a
// server choosing which repository a caller meant.
func (s *Server) requireRepoID(w http.ResponseWriter, r *http.Request, repoID string) bool {
	if strings.TrimSpace(repoID) != "" {
		return true
	}
	s.fail(w, r, InvalidArgument(codemapRepoIDParam, ReasonInvalidValue,
		"`"+codemapRepoIDParam+"` is required: this server can hold the code maps of several repositories"))
	return false
}

// recordRequest decodes the body into the role's request, member by member, so
// that every refusal can NAME the member it is about.
//
// It validates the SHAPE plus the ONE value this surface narrows: `source` must
// be `manual`. The paths themselves are the role's — it normalizes each one and
// refuses an escape above the repository root before it writes anything — so
// they are not inspected here.
func (s *Server) recordRequest(w http.ResponseWriter, r *http.Request) (codemapops.RecordRequest, bool) {
	var request codemapops.RecordRequest

	members, res := decodeJSONObjectBody(w, r)
	if res != nil {
		s.fail(w, r, *res)
		return request, false
	}

	var unknown []string
	for name := range members {
		if !slices.Contains(recordMembers, name) {
			unknown = append(unknown, name)
		}
	}
	if len(unknown) > 0 {
		// One offender, chosen deterministically so a client dispatching on
		// `param` never sees it depend on map order.
		offender := slices.Min(unknown)
		requestInfo(r.Context()).refuse(offender)
		s.fail(w, r, InvalidArgument(offender, ReasonUnknownParameter,
			"this operation's request body carries `"+recordPathsMember+"`, `"+recordSourceMember+"` and nothing else"))
		return request, false
	}

	raw, ok := members[recordPathsMember]
	if !ok {
		s.fail(w, r, InvalidArgument(recordPathsMember, ReasonInvalidValue,
			"`"+recordPathsMember+"` is required"))
		return request, false
	}
	// Through a POINTER so that `null` reaches this refusal rather than
	// unmarshaling as a no-op and being reported downstream as an empty list.
	var paths *[]string
	if err := json.Unmarshal(raw, &paths); err != nil || paths == nil {
		s.fail(w, r, InvalidArgument(recordPathsMember, ReasonInvalidValue,
			"`"+recordPathsMember+"` must be an array of strings"))
		return request, false
	}
	// The document's `minItems: 1`, enforced here because it is a SHAPE rule:
	// the role refuses an empty list too, but as a request-wide ErrValidation
	// that cannot name the member the client has to change.
	if len(*paths) == 0 {
		s.fail(w, r, InvalidArgument(recordPathsMember, ReasonInvalidValue,
			"`"+recordPathsMember+"` must carry at least one path"))
		return request, false
	}
	request.Paths = *paths

	raw, ok = members[recordSourceMember]
	if !ok {
		s.fail(w, r, InvalidArgument(recordSourceMember, ReasonInvalidValue,
			"`"+recordSourceMember+"` is required"))
		return request, false
	}
	var source *string
	if err := json.Unmarshal(raw, &source); err != nil || source == nil {
		s.fail(w, r, InvalidArgument(recordSourceMember, ReasonInvalidValue,
			"`"+recordSourceMember+"` must be a string"))
		return request, false
	}
	// THE ONE VALUE THIS SURFACE NARROWS. `hook`, `commit` and `branch` are
	// observations a local process made about a working tree this server cannot
	// see; a client asserting one over the wire would be fabricating history
	// nobody witnessed. Asserting a link by hand is what `manual` means.
	if codemapops.Source(*source) != codemapops.SourceManual {
		requestInfo(r.Context()).refuse(*source)
		s.fail(w, r, InvalidArgument(recordSourceMember, ReasonInvalidValue,
			"`"+recordSourceMember+"` must be `"+string(codemapops.SourceManual)+"` over this surface"))
		return request, false
	}
	request.Source = codemapops.SourceManual

	return request, true
}

// failCodemapErr answers a failed code-map operation.
//
// It draws the two lines this plane adds to ClassifyError, in the shape
// failMemoryErr draws its one: ErrNotIndexed is a 409 naming the command that
// fixes it, and the role's ErrValidation is a 400. codemapops.ErrValidation is
// an ALIAS of issueops.ErrValidation rather than a second sentinel, so the
// second match is the same identity every other handler here tests.
//
// storage.ErrNotFound needs no line: ClassifyError already maps it to the
// surface's 404.
func (s *Server) failCodemapErr(w http.ResponseWriter, r *http.Request, err error, missing string) {
	var notIndexed *codemapops.ErrNotIndexed
	switch {
	case errors.As(err, &notIndexed):
		s.fail(w, r, CodeMapNotBuilt())
	case errors.Is(err, codemapops.ErrValidation):
		// No `param`: the role refuses a request as a whole — a path that is not
		// repo-relative, a source it does not know, a missing id — and the
		// detail carries its own sentence, which names what to send instead.
		s.fail(w, r, InvalidArgument("", ReasonInvalidValue, err.Error()))
	case errors.Is(err, storage.ErrNotFound) && missing != "":
		// The shared mapping's 404 says "no issue or wisp with that id", which is
		// the wrong sentence for a path. The STATUS and the code are the shared
		// ones; only the detail is this plane's, and each caller passes the
		// sentence for the resource it addressed.
		s.fail(w, r, newResult(CodeNotFound, missing))
	case errors.Is(err, storage.ErrNotFound):
		// An EMPTY sentence means this operation documents no 404, so a miss
		// arriving here is a seam bug rather than an addressable resource — and
		// ClassifyError would turn it into a status the document forbids. It is
		// logged and answered as the internal error it is.
		s.fail(w, r, newResult(CodeInternal, ""))
		s.event("request_error", "request_id", requestInfo(r.Context()).id, "error", err.Error())
	default:
		s.failErr(w, r, err)
	}
}
