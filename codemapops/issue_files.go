package codemapops

import (
	"context"
	"time"
)

// Source classifies how a file-to-issue association was observed.
type Source string

const (
	SourceHook   Source = "hook"
	SourceCommit Source = "commit"
	SourceManual Source = "manual"
	SourceBranch Source = "branch"
)

// RecordRequest asks IssueFiles to note that an issue touched some paths.
type RecordRequest struct {
	IssueID, RepoID string
	Paths           []string
	Source          Source
	CommitSHA       string
}

// RecordResult reports how many issue_files rows were inserted vs. updated.
type RecordResult struct {
	Inserted, Updated int
}

// IssueFile is one path an issue has touched.
type IssueFile struct {
	Path                string
	Source              Source
	CommitSHA           string
	FirstSeen, LastSeen time.Time
	Touches             int
}

// IssueRef is a lightweight reference to another issue, for cross-linking.
type IssueRef struct {
	ID, Title, Status string
}

// IssueCodeFile is one of an issue's touched files, joined with the code map
// context for that file when the repository is indexed.
type IssueCodeFile struct {
	IssueFile
	Context  *FileContext // nil when the repo is not indexed or the path is unknown
	Siblings []IssueRef   // other open issues on this path
}

// IssueCodeContext is the full "what code does this issue touch" answer for
// one issue.
type IssueCodeContext struct {
	IssueID string
	RepoID  string
	Indexed bool
	Shape   *Shape // freshness header; nil when not indexed
	Files   []IssueCodeFile
}

// IssueFiles records and reads the association between issues and the paths
// they touch.
type IssueFiles interface {
	Record(ctx context.Context, req RecordRequest) (RecordResult, error)
	ByIssue(ctx context.Context, issueID string) ([]IssueFile, error)
	ByPath(ctx context.Context, repoID, path string, openOnly bool) ([]IssueRef, error)
	IssueCodeContext(ctx context.Context, issueID, repoID string) (IssueCodeContext, error)
}
