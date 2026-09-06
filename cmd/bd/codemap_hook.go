package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/steveyegge/beads/codemapops"
	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/codemap/cache"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

// The Claude Code tool hooks. PreToolUse tells the agent what the map already
// knows about a file it is about to open; PostToolUse notes that the issue
// being worked touched a file it just edited.
//
// NEITHER MAY EVER FAIL A TOOL CALL. Every path in this file ends in "{}" on
// stdout and exit 0; a failure is at most one line on stderr.
//
// The two halves differ in what they are allowed to cost, and the command is
// split into two subcommands precisely so they can differ. pre-tool runs on
// every Read the agent makes and is annotated to skip store initialization
// entirely (commandOptsOutOfStore walks the ancestor chain, so the annotation
// on this leaf exempts it without exempting its sibling): it reads the derived
// JSON cache and nothing else. post-tool is a write path and opens the store
// like any other write.

// claudeToolHookInput is the subset of Claude Code's PreToolUse/PostToolUse
// payload these hooks use. Unknown fields are ignored by the decoder.
type claudeToolHookInput struct {
	SessionID string `json:"session_id"`
	CWD       string `json:"cwd"`
	ToolName  string `json:"tool_name"`
	ToolInput struct {
		FilePath string `json:"file_path"`
	} `json:"tool_input"`
}

// claudeHookOut is Claude Code's hook response envelope. The zero value
// encodes to "{}", which is the no-op every silent path prints.
type claudeHookOut struct {
	HookSpecificOutput *claudeHookSpecificOutput `json:"hookSpecificOutput,omitempty"`
}

type claudeHookSpecificOutput struct {
	HookEventName     string `json:"hookEventName"`
	AdditionalContext string `json:"additionalContext,omitempty"`
}

// codemapHookMarkerSubdir is the cache subdirectory both halves dedupe under.
const codemapHookMarkerSubdir = "codemap-hooks"

// codemapHookMarkerDirOverride lets tests redirect the marker location.
var codemapHookMarkerDirOverride string

// postToolTimeout bounds the whole post-tool handler, store open included. A
// tool call is a person waiting, so the hook gives up rather than hanging.
const postToolTimeout = 2 * time.Second

var codemapHookCmd = &cobra.Command{
	Use:    "codemap-hook",
	Hidden: true,
	Short:  "Run an internal Claude Code tool hook for the code map",
}

var codemapHookPreToolCmd = &cobra.Command{
	Use:    "pre-tool",
	Hidden: true,
	Short:  "Inject what the code map knows about the file a tool is about to touch",
	// The whole point of this hook is that it costs nothing: it reads the
	// derived cache and never opens the store.
	Annotations:   map[string]string{skipStoreAnnotation: "1"},
	Args:          cobra.NoArgs,
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(_ *cobra.Command, _ []string) error {
		return runCodemapPreToolHook(os.Stdin, os.Stdout)
	},
}

var codemapHookPostToolCmd = &cobra.Command{
	Use:           "post-tool",
	Hidden:        true,
	Short:         "Record that the issue being worked touched the file a tool just edited",
	Args:          cobra.NoArgs,
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, _ []string) error {
		return runCodemapPostToolHook(cmd.Context(), os.Stdin, os.Stdout)
	},
}

func init() {
	codemapHookCmd.AddCommand(codemapHookPreToolCmd, codemapHookPostToolCmd)
	rootCmd.AddCommand(codemapHookCmd)
}

// =============================================================================
// PreToolUse
// =============================================================================

func runCodemapPreToolHook(stdin io.Reader, stdout io.Writer) error {
	in, ok := decodeToolHookInput(stdin)
	if !ok {
		return emitHookOut(stdout, "")
	}
	rel := codemapRelPath(in.CWD, in.ToolInput.FilePath)
	if rel == "" {
		return emitHookOut(stdout, "")
	}
	// One injection per file per session: the second time the agent opens a
	// file it has already been told about, the context is noise.
	//
	// The marker is checked BEFORE the cache is read, because in a long
	// session it is the common answer and the cache is the expensive read —
	// this repository's own is 1.4 MB of JSON, some 10 ms to parse. Checking
	// it first is one stat instead.
	marker := agentHookMarkerPath(codemapHookMarkerBaseDir(), in.SessionID, rel)
	if _, err := os.Stat(marker); err == nil {
		return emitHookOut(stdout, "")
	}
	beadsDir := beads.FindBeadsDirFrom(in.CWD)
	if beadsDir == "" {
		return emitHookOut(stdout, "")
	}
	// A missing or unreadable cache is the ordinary state of a repository
	// whose map was never built. It is silence, not a warning on every tool
	// call the agent makes.
	f, err := cache.Read(beadsDir)
	if err != nil {
		return emitHookOut(stdout, "")
	}
	text := preToolContext(f, rel)
	if text == "" {
		return emitHookOut(stdout, "")
	}
	if err := writeAgentHookMarker(marker); err != nil {
		// Injecting twice beats injecting never, so a marker we could not
		// write does not suppress the context.
		fmt.Fprintf(os.Stderr, "beads: codemap pre-tool marker: %v\n", err)
	}
	return emitHookOut(stdout, text)
}

// preToolContext renders at most four lines about one path, or "" when the map
// has nothing to say. It is deliberately terse: this text is prepended to a
// prompt on every first read of every file, and `bd codemap show` is one line
// away for anything longer.
func preToolContext(f cache.File, relPath string) string {
	e, ok := f.Files[relPath]
	if !ok {
		return ""
	}
	var lines []string

	head := "[codemap] " + relPath
	var tags []string
	if e.Layer != "" {
		tags = append(tags, e.Layer)
	}
	if e.Stale {
		tags = append(tags, "summary stale")
	}
	if len(tags) > 0 {
		head += " (" + strings.Join(tags, ", ") + ")"
	}
	if e.Summary != "" {
		head += ": " + e.Summary
	}
	lines = append(lines, head)

	var edges []string
	if len(e.Imports) > 0 {
		edges = append(edges, "imports: "+strings.Join(capNames(e.Imports), ", "))
	}
	if len(e.Importers) > 0 {
		edges = append(edges, "imported by "+strings.Join(capNames(e.Importers), ", "))
	}
	if len(edges) > 0 {
		lines = append(lines, "  "+strings.Join(edges, " · "))
	}

	if len(e.OpenIssues) > 0 {
		refs := e.OpenIssues
		suffix := ""
		if len(refs) > preToolMaxNames {
			refs, suffix = refs[:preToolMaxNames], fmt.Sprintf(" (+%d more)", len(e.OpenIssues)-preToolMaxNames)
		}
		parts := make([]string, 0, len(refs))
		for _, r := range refs {
			parts = append(parts, fmt.Sprintf("%s (%s) %s", r.ID, r.Status, r.Title))
		}
		lines = append(lines, "  open issues here: "+strings.Join(parts, "; ")+suffix)
	}

	lines = append(lines, "  more: bd codemap show "+relPath)
	return strings.Join(lines, "\n")
}

// preToolMaxNames caps each list on a context line. The cache already caps its
// own lists at 12; a prompt line wants far fewer.
const preToolMaxNames = 3

func capNames(names []string) []string {
	if len(names) <= preToolMaxNames {
		return names
	}
	out := append([]string{}, names[:preToolMaxNames]...)
	return append(out, fmt.Sprintf("+%d more", len(names)-preToolMaxNames))
}

// =============================================================================
// PostToolUse
// =============================================================================

// postToolTools is the set of tools that CHANGE a file. A Read tells us nothing
// about what an issue touched.
var postToolTools = map[string]bool{"Edit": true, "Write": true, "MultiEdit": true, "NotebookEdit": true}

func postToolTracksTool(name string) bool { return postToolTools[name] }

func runCodemapPostToolHook(ctx context.Context, stdin io.Reader, stdout io.Writer) error {
	if err := recordPostToolEdit(ctx, stdin); err != nil {
		fmt.Fprintf(os.Stderr, "beads: codemap post-tool: %v\n", err)
	}
	// Always "{}": PostToolUse has nothing to say to the model, and a tool call
	// is never worth failing over a bookkeeping miss.
	return emitHookOut(stdout, "")
}

// recordPostToolEdit is the part that can fail. A nil error means either "we
// recorded it" or "there was correctly nothing to record"; the distinction does
// not reach the agent either way.
func recordPostToolEdit(ctx context.Context, stdin io.Reader) error {
	in, ok := decodeToolHookInput(stdin)
	if !ok || !postToolTracksTool(in.ToolName) {
		return nil
	}
	// The active issue comes from the last-touched file, which is resolved from
	// THIS PROCESS's working directory rather than from the payload's cwd.
	// Claude Code runs hooks in the project directory, so the two agree; a
	// harness that did not would simply record nothing.
	id := GetLastTouchedID()
	if id == "" {
		return nil
	}
	rel := codemapRelPath(in.CWD, in.ToolInput.FilePath)
	if rel == "" {
		return nil
	}
	// Once per (session, issue, path): an agent editing one file twenty times
	// should not cost twenty write transactions.
	marker := agentHookMarkerPath(codemapHookMarkerBaseDir(), in.SessionID+"/"+id, rel)
	if _, err := os.Stat(marker); err == nil {
		return nil
	}

	ctx, cancel := context.WithTimeout(ctx, postToolTimeout)
	defer cancel()

	reader, err := openIssueReader()
	if err != nil {
		return err
	}
	details, err := reader.Get(ctx, issueops.GetRequest{ID: id, BriefDeps: true})
	if err != nil {
		return fmt.Errorf("reading %s: %w", id, err)
	}
	// ONLY while the issue is actually being worked. An open issue that merely
	// happens to be the last one touched is not what this edit is about, and
	// attributing files to it would poison the link table quietly.
	if details == nil || details.Status != types.StatusInProgress {
		return nil
	}

	_, repoID, err := codemapRepoRoot()
	if err != nil {
		return err
	}
	files, err := openIssueFiles()
	if err != nil {
		return err
	}
	if _, err := files.Record(ctx, codemapops.RecordRequest{
		IssueID: id, RepoID: repoID, Paths: []string{rel}, Source: codemapops.SourceHook,
	}); err != nil {
		return fmt.Errorf("recording %s %s: %w", id, rel, err)
	}
	commandDidWrite.Store(true)
	if err := writeAgentHookMarker(marker); err != nil {
		return fmt.Errorf("marker: %w", err)
	}
	return nil
}

// =============================================================================
// Shared
// =============================================================================

func codemapHookMarkerBaseDir() string {
	return agentHookMarkerBaseDir(codemapHookMarkerSubdir, codemapHookMarkerDirOverride)
}

// decodeToolHookInput reads the payload. A malformed payload is not an error
// worth reporting: it means the harness sent us something we do not understand,
// and the answer to that is silence.
func decodeToolHookInput(stdin io.Reader) (claudeToolHookInput, bool) {
	var in claudeToolHookInput
	if err := json.NewDecoder(stdin).Decode(&in); err != nil && err != io.EOF {
		return in, false
	}
	return in, true
}

func emitHookOut(stdout io.Writer, additionalContext string) error {
	out := claudeHookOut{}
	if additionalContext != "" {
		out.HookSpecificOutput = &claudeHookSpecificOutput{
			HookEventName:     "PreToolUse",
			AdditionalContext: additionalContext,
		}
	}
	return json.NewEncoder(stdout).Encode(out)
}

// codemapRelPath turns the tool's file_path into the slash-separated,
// repository-relative key the cache and the link table are both keyed by.
// Anything it cannot place inside the repository is "": not our file.
func codemapRelPath(cwd, filePath string) string {
	if filePath == "" || cwd == "" {
		return ""
	}
	if !filepath.IsAbs(filePath) {
		filePath = filepath.Join(cwd, filePath)
	}
	root, err := gitToplevelAt(cwd)
	if err != nil {
		return ""
	}
	// BOTH sides through EvalSymlinks before comparing. On macOS a repository
	// under /var is really under /private/var, and git reports the resolved
	// path while the agent reports the one the user typed; a plain Rel between
	// the two produces a "../../.." that matches nothing.
	rel, err := filepath.Rel(resolveSymlinks(root), resolveSymlinks(filePath))
	if err != nil {
		return ""
	}
	rel = filepath.ToSlash(rel)
	if rel == ".." || strings.HasPrefix(rel, "../") || filepath.IsAbs(rel) {
		return ""
	}
	return rel
}

// gitToplevelAt asks git for the working tree root containing dir. This is the
// ONE subprocess pre-tool runs, and it is what makes the hook correct in a
// worktree, a submodule, and a repository whose .beads lives elsewhere.
func gitToplevelAt(dir string) (string, error) {
	cmd := exec.Command("git", "rev-parse", "--show-toplevel")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	root := strings.TrimSpace(string(out))
	if root == "" {
		return "", fmt.Errorf("not a git repository: %s", dir)
	}
	return root, nil
}

// resolveSymlinks best-effort canonicalizes p. A path that does not exist yet
// (a Write creating a new file) still has an existing directory, so the
// directory is resolved and the base name rejoined.
func resolveSymlinks(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	if d, err := filepath.EvalSymlinks(filepath.Dir(p)); err == nil {
		return filepath.Join(d, filepath.Base(p))
	}
	return filepath.Clean(p)
}
