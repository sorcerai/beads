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
	IssueID   string   `json:"issue_id"`
	RepoID    string   `json:"repo_id"`
	Paths     []string `json:"paths,omitempty"`
	Source    Source   `json:"source"`
	CommitSHA string   `json:"commit_sha,omitempty"`
}

// RecordResult reports how many issue_files rows were inserted vs. updated.
type RecordResult struct {
	Inserted int `json:"inserted"`
	Updated  int `json:"updated"`
}

// IssueFile is one path an issue has touched.
type IssueFile struct {
	Path      string    `json:"path"`
	Source    Source    `json:"source"`
	CommitSHA string    `json:"commit_sha,omitempty"`
	FirstSeen time.Time `json:"first_seen"`
	LastSeen  time.Time `json:"last_seen"`
	Touches   int       `json:"touches"`
}

// IssueRef is a lightweight reference to another issue, for cross-linking.
type IssueRef struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Status string `json:"status"`
}

// IssueCodeFile is one of an issue's touched files, joined with the code map
// context for that file when the repository is indexed.
type IssueCodeFile struct {
	IssueFile
	Context  *FileContext `json:"context,omitempty"`  // nil when the repo is not indexed or the path is unknown
	Siblings []IssueRef   `json:"siblings,omitempty"` // other open issues on this path
}

// IssueCodeContext is the full "what code does this issue touch" answer for
// one issue.
type IssueCodeContext struct {
	IssueID string          `json:"issue_id"`
	RepoID  string          `json:"repo_id"`
	Indexed bool            `json:"indexed"`
	Shape   *Shape          `json:"shape,omitempty"` // freshness header; nil when not indexed
	Files   []IssueCodeFile `json:"files,omitempty"`
}

// IssueFiles records and reads the association between issues and the paths
// they touch.
type IssueFiles interface {
	Record(ctx context.Context, req RecordRequest) (RecordResult, error)
	ByIssue(ctx context.Context, issueID string) ([]IssueFile, error)
	ByPath(ctx context.Context, repoID, path string, openOnly bool) ([]IssueRef, error)
	IssueCodeContext(ctx context.Context, issueID, repoID string) (IssueCodeContext, error)
}
