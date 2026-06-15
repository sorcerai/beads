package main

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/debug"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/issueops"
)

// transact wraps store.RunInTransaction and marks that a transactional
// DOLT_COMMIT occurred, preventing the redundant maybeAutoCommit in
// PersistentPostRun. Use this instead of calling store.RunInTransaction
// directly from command handlers.
func transact(ctx context.Context, s storage.DoltStorage, commitMsg string, fn func(tx storage.Transaction) error) error {
	err := s.RunInTransaction(ctx, commitMsg, fn)
	if err == nil {
		commandDidExplicitDoltCommit = true
	}
	return err
}

// transactHonoringAutoCommit wraps transactional CLI writes whose Dolt commit is
// part of command auto-commit policy. In embedded batch/off modes the SQL
// transaction still commits, but no Dolt version commit is created.
func transactHonoringAutoCommit(ctx context.Context, s storage.DoltStorage, commitMsg string, fn func(tx storage.Transaction) error) error {
	msg := commitMsg
	committedExplicitly := strings.TrimSpace(msg) != ""
	if isEmbeddedMode() {
		mode, err := getDoltAutoCommitMode()
		if err != nil {
			return err
		}
		if mode != doltAutoCommitOn {
			msg = ""
			committedExplicitly = false
		}
	}

	err := s.RunInTransaction(ctx, msg, fn)
	if err == nil && committedExplicitly {
		commandDidExplicitDoltCommit = true
	}
	return err
}

type doltAutoCommitParams struct {
	// Command is the top-level bd command name (e.g., "create", "update").
	Command string
	// IssueIDs are the primary issue IDs affected by the command (optional).
	IssueIDs []string
	// MessageOverride, if non-empty, is used verbatim.
	MessageOverride string
}

// maybeAutoCommit creates a Dolt commit after a successful write command when enabled.
//
// Semantics:
//   - Only applies when dolt auto-commit is "on" AND the active store is versioned (Dolt).
//   - Skips SQL server modes; the server owns transaction commit lifecycle there.
//   - In "batch" mode, commits are deferred — changes accumulate in the working set
//     until an explicit commit point (bd dolt commit).
//   - Uses Dolt's "commit all" behavior under the hood (DOLT_COMMIT -Am).
//   - Treats "nothing to commit" as a no-op.
func maybeAutoCommit(ctx context.Context, p doltAutoCommitParams) error {
	if !isEmbeddedMode() {
		return nil
	}
	return maybeAutoCommitStore(ctx, getStore(), p)
}

func commitPendingIfEmbedded(ctx context.Context, st storage.DoltStorage, actor string, p doltAutoCommitParams) error {
	if !isEmbeddedMode() || st == nil {
		return nil
	}
	if strings.TrimSpace(p.MessageOverride) == "" {
		p.MessageOverride = formatDoltAutoCommitMessage(p.Command, actor, p.IssueIDs)
	}
	return maybeAutoCommitStore(ctx, st, p)
}

func maybeAutoCommitStore(ctx context.Context, st storage.DoltStorage, p doltAutoCommitParams) error {
	mode, err := getDoltAutoCommitMode()
	if err != nil {
		return err
	}
	// In batch mode, skip per-command commits. Changes stay in the working set
	// and are committed at logical boundaries (bd dolt commit).
	if mode != doltAutoCommitOn {
		return nil
	}

	if st == nil {
		return nil
	}
	if lm, ok := storage.UnwrapStore(st).(storage.LifecycleManager); ok && lm.IsClosed() {
		return nil
	}

	msg := p.MessageOverride
	if strings.TrimSpace(msg) == "" {
		msg = formatDoltAutoCommitMessage(p.Command, getActor(), p.IssueIDs)
	}

	if err := st.Commit(ctx, msg); err != nil {
		if isDoltNothingToCommit(err) {
			return nil
		}
		return err
	}
	return nil
}

// autoCommitSweepExemptPaths are inspection commands that display version
// control or working-set state. The unflagged-writes sweep must never run
// after them: bd dolt status would commit the dirty state it just displayed,
// destroying the inspect-before-commit flow (bd-578h9.7). Keyed by full
// command path because leaf names like "status" collide across parents.
var autoCommitSweepExemptPaths = map[string]bool{
	"bd dolt status": true,
	"bd vc status":   true,
	"bd diff":        true,
	"bd history":     true,
}

// autoCommitSweepExempt reports whether cmd must not trigger the
// dirty-working-set sweep (bd-578h9.7). Read-only commands are exempt for a
// second reason: they open the embedded store read-only, so the sweep's
// commit would fail with errReadOnly and turn a successful read into a fatal
// error. Explicitly flagged writes (commandDidWrite) still auto-commit.
func autoCommitSweepExempt(cmd *cobra.Command) bool {
	return isReadOnlyCommand(cmd.Name()) || autoCommitSweepExemptPaths[cmd.CommandPath()]
}

// formatDoltSweepCommitMessage attributes a sweep commit distinctly from a
// normal auto-commit: the swept changes belong to an EARLIER command that
// failed (or forgot commandDidWrite) before its own auto-commit could run —
// blaming them on the command that merely triggered the sweep corrupts the
// audit trail (bd-578h9.7).
func formatDoltSweepCommitMessage(cmd, actor string) string {
	cmd = strings.TrimSpace(cmd)
	if cmd == "" {
		cmd = "write"
	}
	actor = strings.TrimSpace(actor)
	if actor == "" {
		actor = "unknown"
	}
	return fmt.Sprintf("bd: autocommit sweep of earlier uncommitted changes (after %s by %s)", cmd, actor)
}

// workingSetHasUnflaggedWrites reports whether the embedded working set holds
// committable changes even though no write path set commandDidWrite. It is the
// safety net behind that flag: a mutating command that forgets to set it would
// otherwise leave its writes to be swept into the NEXT command's auto-commit
// with wrong attribution (bd-6dnrw.11). Only meaningful in embedded mode with
// auto-commit "on" — batch mode keeps the working set dirty by design.
func workingSetHasUnflaggedWrites(ctx context.Context, cmdName string) bool {
	if !isEmbeddedMode() {
		return false
	}
	if mode, err := getDoltAutoCommitMode(); err != nil || mode != doltAutoCommitOn {
		return false
	}
	st := getStore()
	if st == nil {
		return false
	}
	unwrapped := storage.UnwrapStore(st)
	if lm, ok := unwrapped.(storage.LifecycleManager); ok && lm.IsClosed() {
		return false
	}
	checker, ok := unwrapped.(interface {
		HasPendingChanges(ctx context.Context) (bool, error)
	})
	if !ok {
		return false
	}
	dirty, err := checker.HasPendingChanges(ctx)
	if err != nil || !dirty {
		return false
	}
	debug.Logf("command %q left uncommitted changes without setting commandDidWrite; auto-committing anyway (bd-6dnrw.11)", cmdName)
	return true
}

func isDoltNothingToCommit(err error) bool {
	return issueops.IsNothingToCommitError(err)
}

func formatDoltAutoCommitMessage(cmd string, actor string, issueIDs []string) string {
	cmd = strings.TrimSpace(cmd)
	if cmd == "" {
		cmd = "write"
	}
	actor = strings.TrimSpace(actor)
	if actor == "" {
		actor = "unknown"
	}

	ids := make([]string, 0, len(issueIDs))
	seen := make(map[string]bool, len(issueIDs))
	for _, id := range issueIDs {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		ids = append(ids, id)
	}
	slices.Sort(ids)

	const maxIDs = 5
	if len(ids) > maxIDs {
		ids = ids[:maxIDs]
	}

	if len(ids) == 0 {
		return fmt.Sprintf("bd: %s (auto-commit) by %s", cmd, actor)
	}
	return fmt.Sprintf("bd: %s (auto-commit) by %s [%s]", cmd, actor, strings.Join(ids, ", "))
}
