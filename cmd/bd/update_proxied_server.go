package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/steveyegge/beads/internal/hooks"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/storage/fs"
	storageissueops "github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/storage/uow"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/ui"
	"github.com/steveyegge/beads/issueops"
)

// proxiedUpdateRetryMaxElapsed bounds the whole-attempt retry loop for one
// issue's update. A var so tests can shrink it when exercising conflict
// exhaustion; it tracks the shared default rather than restating it.
var proxiedUpdateRetryMaxElapsed = uow.DefaultTxRetryMaxElapsed

func runUpdateProxiedServer(cmd *cobra.Command, ctx context.Context, args []string) error {
	if len(args) == 0 {
		return HandleErrorRespectJSON("no issue ID provided")
	}

	in, err := gatherUpdateInput(ctx, cmd)
	if err != nil {
		return err
	}
	if isUpdateInputNoop(in) {
		fmt.Println("No updates specified")
		return nil
	}

	// Derive success-output format from the global JSON decision (--json OR
	// --format json OR config), the same signal reportUpdateFailures uses, so
	// success output and the failure report never disagree on format within one
	// invocation. This matches the non-proxied path in update.go.
	jsonOut := jsonOutput
	var updated []*types.Issue
	// failures accumulates every requested ID that could not be updated —
	// generic per-ID errors as well as a lost --claim race. In a mixed batch a
	// later winner must NOT flip the exit code back to success and hide the
	// failed IDs from exit-code automation; report them all and exit non-zero,
	// mirroring the non-proxied path in update.go (beads audit finding #10).
	var failures []updateIDFailure

	for _, id := range args {
		issue, fail, err := applyUpdateProxiedOne(ctx, id, in)
		if err != nil {
			return err
		}
		if fail != nil {
			failures = append(failures, *fail)
			continue
		}
		if jsonOut {
			updated = append(updated, issue)
		} else {
			fmt.Printf("%s Updated issue: %s\n", ui.RenderPass("✓"), formatFeedbackID(issue.ID, issue.Title))
		}
	}

	if jsonOut && len(updated) > 0 {
		_ = outputJSON(updated)
	}
	if len(failures) > 0 {
		return reportUpdateFailures(failures, len(args))
	}
	return nil
}

// proxiedUpdateAttempt is what one read-merge-write attempt hands back to the
// shared retry loop. Exactly one of issue and fail is set; before and
// notesOverwritten feed the reporting that must run once per landed write
// rather than once per attempt.
type proxiedUpdateAttempt struct {
	issue            *types.Issue
	before           *types.Issue
	fail             *updateIDFailure
	notesOverwritten bool
}

// uowStageProvider records the errors the two stages the shared retry loop
// owns — opening the unit of work and committing it — hand back on the most
// recent attempt, so applyUpdateProxiedOne can attribute the error it gets
// from uow.RunTxResultWithin to the stage that produced it. The bespoke loop
// this replaced attributed errors inline, at the site that produced them; the
// recorded errors put attribution back on identity rather than on guesswork
// about what an error looks like. Both fields are reset on every NewUOW, so
// they describe the final attempt only.
type uowStageProvider struct {
	uow.UnitOfWorkProvider
	newUOWErr error
	commitErr error
}

func (p *uowStageProvider) NewUOW(ctx context.Context) (uow.UnitOfWork, error) {
	uw, err := p.UnitOfWorkProvider.NewUOW(ctx)
	p.newUOWErr, p.commitErr = err, nil
	if err != nil {
		return uw, err
	}
	return &uowStageRecorder{UnitOfWork: uw, provider: p}, nil
}

type uowStageRecorder struct {
	uow.UnitOfWork
	provider *uowStageProvider
}

func (u *uowStageRecorder) Commit(ctx context.Context, message string) error {
	err := u.UnitOfWork.Commit(ctx, message)
	u.provider.commitErr = err
	return err
}

// applyUpdateProxiedOne applies one issue's update through uow.RunTxResultWithin,
// which redoes the WHOLE read-merge-write in a fresh unit of work when Dolt
// reports a serialization failure. It is the retry/commit implementation every
// unit-of-work write path shares: uow.RunTx and uow.RunTxResult both delegate
// to it.
//
// The retry must wrap the whole attempt, never just the commit: a
// serialization failure means the server already rolled the transaction back,
// so re-committing the same session (the old uow.CommitWithRetries call) can
// only ever produce "nothing to commit" — which the old code swallowed,
// printing "✓ Updated" and exiting 0 for a write that was silently lost.
// Redoing the attempt re-reads the winner's committed row, so merge
// operations (metadata edits, note appends) resolve against authoritative
// state instead of erasing it.
func applyUpdateProxiedOne(ctx context.Context, id string, in *updateInput) (*types.Issue, *updateIDFailure, error) {
	if uowProvider == nil {
		return nil, nil, HandleError("proxied-server UOW provider not initialized")
	}
	if in.claim {
		return applyClaimProxiedOne(ctx, id, in)
	}

	provider := &uowStageProvider{UnitOfWorkProvider: uowProvider}
	attempt, err := uow.RunTxResultWithin(ctx, provider, proxiedUpdateRetryMaxElapsed,
		func(ctx context.Context, uw uow.UnitOfWork) (proxiedUpdateAttempt, string, error) {
			return applyUpdateProxiedAttempt(ctx, uw, id, in)
		})
	if err != nil {
		// The retry loop hands back the error a stage returned, unchanged
		// (backoff unwraps its Permanent envelope), so the errors recorded by
		// provider identify the stage by identity. The one error no stage
		// produced is the context's own: backoff substitutes ctx.Err() when
		// cancellation cuts the loop short between attempts.
		switch {
		case provider.newUOWErr != nil && errors.Is(err, provider.newUOWErr):
			fmt.Fprintf(os.Stderr, "Error opening unit of work for %s: %v\n", id, err)
			return nil, &updateIDFailure{ID: id, Error: fmt.Sprintf("opening unit of work: %v", err)}, nil
		case uow.IsSerializationError(err):
			// Retries exhausted while losing Dolt's commit-time merge. The
			// write did NOT land; fail loudly instead of exiting 0.
			fmt.Fprintf(os.Stderr, "Error updating %s: retries exhausted on write conflicts: %v\n", id, err)
			return nil, &updateIDFailure{ID: id, Error: fmt.Sprintf("retries exhausted on write conflicts: %v", err)}, nil
		case provider.commitErr != nil && errors.Is(err, provider.commitErr):
			fmt.Fprintf(os.Stderr, "Error committing %s: %v\n", id, err)
			return nil, &updateIDFailure{ID: id, Error: fmt.Sprintf("committing: %v", err)}, nil
		case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
			// Cancellation cut the retry loop short between attempts (SIGINT
			// cancels bd's root context). That is not a per-issue verdict:
			// abort the whole batch, as the loop this replaced did. A commit
			// that itself failed with a context error is NOT this case — it is
			// a per-ID commit failure, caught by the arm above.
			return nil, nil, err
		default:
			// Unreachable today: the attempt returns terminal per-issue
			// failures as an attempt result, never as an error, so the only
			// errors it can produce are serialization failures. Attribute
			// anything new it grows to the update, which is where it came from.
			fmt.Fprintf(os.Stderr, "Error updating %s: %v\n", id, err)
			return nil, &updateIDFailure{ID: id, Error: fmt.Sprintf("updating: %v", err)}, nil
		}
	}
	if attempt.fail != nil {
		return nil, attempt.fail, nil
	}

	// Post-commit reporting: the write has landed (or was the legitimately
	// empty working set of a wisp-only update, which RunTxResultWithin
	// tolerates), so these run exactly once no matter how many attempts the
	// conflict retry burned.
	if attempt.notesOverwritten {
		warnNotesReplacement(id)
	}
	if err := fireProxiedUpdateHooks(ctx, attempt.before, attempt.issue); err != nil {
		fmt.Fprintf(os.Stderr, "warning: %s: %v\n", id, err)
	}
	// Post-close lifecycle hook on an actual open->closed transition, mirroring
	// direct-mode `bd update --status closed`. Proxied mode holds a UOW, not a
	// store, so the hook resolves from cwd. --no-hooks has no proxied flag; the
	// BD_NO_CLOSE_HOOK env guard inside firePostCloseHook is the opt-out.
	if attempt.before != nil && attempt.issue != nil &&
		attempt.before.Status != types.StatusClosed &&
		attempt.issue.Status == types.StatusClosed {
		firePostCloseHook(ctx, nil, []string{attempt.issue.ID})
	}
	return attempt.issue, nil, nil
}

// proxiedIssueLifecycle asks the unit-of-work provider for the write role, the
// same way proxiedIssueReader asks it for the read one. The accessor is the
// door: a provider that cannot answer says so rather than being wired around.
func proxiedIssueLifecycle() (issueops.Lifecycle, error) {
	if uowProvider == nil {
		return nil, errors.New("proxied-server UOW provider not initialized")
	}
	src, ok := uowProvider.(uow.IssueLifecycleSource)
	if !ok {
		return nil, fmt.Errorf("proxied-server provider %T does not offer the issue-lifecycle surface", uowProvider)
	}
	return src.IssueLifecycle()
}

// applyClaimProxiedOne applies one issue's --claim update through
// issueops.Lifecycle rather than through a hand-rolled attempt.
//
// THE CLAIM IS NOT IMPLEMENTED HERE, and that is the point. The CAS, the
// eligibility rules, the claim-pool case, the issue-or-wisp resolve, the
// whole-attempt retry, the commit and the exact text of both refusals belong to
// the contract, which `bd serve`'s claim endpoint now reaches through the same
// call. What stays here is this surface's own protocol: the template guard, the
// per-id failure taxonomy the multi-id batch needs, the notes-overwrite warning
// and the completion hooks — none of which the HTTP surface has, and none of
// which is a claim.
//
// Provenance carries the commit message this path has always written, so `bd
// dolt log` reads the same after the move as before it. The plane is
// deliberately NOT restricted: `bd update --claim` has always resolved a wisp
// id, unlike the HTTP endpoint, which serves durable issues only.
//
// The pre-read is a read of its own, one transaction earlier than the claim, so
// the template guard is advisory in a way it was not when this file did the
// read itself. It guards a command-shaped policy, not an invariant — the
// mutation's own guards are all inside the contract's transaction — and paying
// for it is what lets the claim itself have exactly one implementation.
func applyClaimProxiedOne(ctx context.Context, id string, in *updateInput) (*types.Issue, *updateIDFailure, error) {
	ops, err := proxiedIssueLifecycle()
	if err != nil {
		return nil, nil, HandleError("%v", err)
	}
	before, fail := proxiedClaimTarget(ctx, id)
	if fail != nil {
		return nil, fail, nil
	}
	patch, err := proxiedUpdatePatch(in, before)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error updating %s: %v\n", id, err)
		return nil, &updateIDFailure{ID: id, Error: fmt.Sprintf("updating: %v", err)}, nil
	}
	notesOverwritten := replacesExistingNotes(before.Notes, in.fields)

	result, err := runCommandUpdateMutation(ctx, ops, commandUpdateMutation{
		actor:   actor,
		issueID: id,
		patch:   patch,
		claim:   true,
		force:   in.force,
		// The guards are nil by construction: gatherUpdateInput refuses
		// --if-assignee/--if-status alongside --claim, which is also what the
		// request contract requires of a claim.
		provenance: fmt.Sprintf("bd: update %s", id),
	})
	if err != nil {
		// Cancellation is not a verdict on this issue — SIGINT cancels bd's
		// root context, and every remaining id in the batch would fail the same
		// way. Abort the batch, as the non-claim path above still does. The
		// contract owns the unit of work now, so a commit that itself failed
		// with a context error is indistinguishable from a cancellation between
		// attempts; aborting is the right call for both.
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, nil, err
		}
		return nil, proxiedClaimFailure(id, err), nil
	}
	updated := result.Issue
	if updated == nil {
		fmt.Fprintf(os.Stderr, "Error updating %s: the claim reported no issue\n", id)
		return nil, &updateIDFailure{ID: id, Error: "updating: no issue returned"}, nil
	}
	// `bd update` has never printed dependency records; the direct route drops
	// them for the same reason.
	updated.Dependencies = nil
	// A CLAIM answers an already-published surface, and that surface is the
	// bare row. issueops.ClaimResult says so outright — labels, dependency
	// records and comments omitted, and "enriching it is a decision for the
	// next revision window, not a side effect of moving the operation onto a
	// role." Routing this path through Lifecycle hydrates labels, which is
	// exactly that side effect, so they come back off here.
	//
	// This is what keeps `bd update --claim --json` byte-identical to the v0
	// claim response, which TestProxiedServerServeClaim asserts with an EMPTY
	// allowlist: any difference at all between the two surfaces is a real
	// divergence. The non-claim update path above is untouched and still
	// carries labels.
	updated.Labels = nil
	updated.Comments = nil

	// Post-commit reporting: the write has landed, so these run exactly once no
	// matter how many attempts the contract's conflict retry burned.
	if notesOverwritten {
		warnNotesReplacement(id)
	}
	if err := fireProxiedUpdateHooks(ctx, before, updated); err != nil {
		fmt.Fprintf(os.Stderr, "warning: %s: %v\n", id, err)
	}
	return updated, nil, nil
}

// proxiedClaimTarget reads the row a claim is about through the query role, for
// the three things the claim's own result cannot answer: whether the target is
// a template, whether --notes is about to replace existing notes, and whether
// the update closes an open issue (which fires a second hook).
func proxiedClaimTarget(ctx context.Context, id string) (*types.Issue, *updateIDFailure) {
	rd, err := proxiedIssueReader()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error resolving %s: %v\n", id, err)
		return nil, &updateIDFailure{ID: id, Error: fmt.Sprintf("resolving issue: %v", err)}
	}
	details, err := rd.Get(ctx, issueops.GetRequest{ID: id})
	if err != nil {
		if errors.Is(err, issueops.ErrNotFound) {
			fmt.Fprintf(os.Stderr, "Issue %s not found\n", id)
			return nil, &updateIDFailure{ID: id, Error: "issue not found"}
		}
		fmt.Fprintf(os.Stderr, "Error resolving %s: %v\n", id, err)
		return nil, &updateIDFailure{ID: id, Error: fmt.Sprintf("resolving issue: %v", err)}
	}
	current := &details.Issue
	if err := validateIssueUpdatable(id, current); err != nil {
		fmt.Fprintf(os.Stderr, "%s\n", err)
		return nil, &updateIDFailure{ID: id, Error: err.Error()}
	}
	return current, nil
}

// proxiedClaimFailure sorts a refused claim into the same per-id verdicts, with
// the same copy, the hand-rolled attempt below produces. A guard refusal sets
// GuardMismatch so the batch exits 13 rather than 1.
//
// The one verdict it cannot reproduce is stage attribution: "opening unit of
// work" and "committing" were told apart by watching the provider this path no
// longer owns, so both now read as the generic update failure. That is the
// price of the contract owning the transaction, and it costs a word in the
// message, not a verdict — the id still fails, loudly and non-zero.
func proxiedClaimFailure(id string, err error) *updateIDFailure {
	switch {
	case errors.Is(err, storage.ErrNotFound):
		fmt.Fprintf(os.Stderr, "Issue %s not found\n", id)
		return &updateIDFailure{ID: id, Error: "issue not found"}
	case errors.Is(err, storage.ErrAlreadyClaimed), errors.Is(err, storage.ErrNotClaimable):
		fmt.Fprintf(os.Stderr, "Error claiming %s: %v\n", id, err)
		return &updateIDFailure{ID: id, Error: fmt.Sprintf("claiming issue: %v", err)}
	case errors.Is(err, storage.ErrCloseOpenChildren):
		fmt.Fprintf(os.Stderr, "%v\n", err)
		return &updateIDFailure{ID: id, Error: err.Error()}
	case errors.Is(err, storage.ErrCloseBlocked):
		fmt.Fprintf(os.Stderr, "%v (use --force to override)\n", err)
		return &updateIDFailure{ID: id, Error: fmt.Sprintf("%v (use --force to override)", err)}
	case uow.IsSerializationError(err):
		// The contract spent its retry budget losing Dolt's commit-time merge.
		// The write did NOT land; fail loudly instead of exiting 0.
		fmt.Fprintf(os.Stderr, "Error updating %s: retries exhausted on write conflicts: %v\n", id, err)
		return &updateIDFailure{ID: id, Error: fmt.Sprintf("retries exhausted on write conflicts: %v", err)}
	case isGuardMismatch(err):
		fmt.Fprintf(os.Stderr, "Error updating %s: %v\n", id, err)
		return &updateIDFailure{ID: id, Error: fmt.Sprintf("precondition failed: %v", err), GuardMismatch: true}
	default:
		fmt.Fprintf(os.Stderr, "Error updating %s: %v\n", id, err)
		return &updateIDFailure{ID: id, Error: fmt.Sprintf("updating: %v", err)}
	}
}

// proxiedUpdatePatch reshapes gathered CLI input into the contract's typed
// patch. The field map goes through the same builder the direct route uses; the
// edits this input keeps beside that map (labels, reparent, the merge-shaped
// note and metadata edits) are folded in here, so both routes end up describing
// the same mutation.
//
// The merge-shaped edits stay merge-shaped: the contract resolves them against
// the row re-read inside the mutation transaction. Pre-merging them against
// `before` is what silently erased keys a concurrent writer had committed.
func proxiedUpdatePatch(in *updateInput, before *types.Issue) (issueops.IssuePatch, error) {
	patch, err := buildUpdatePatch(in.fields)
	if err != nil {
		return issueops.IssuePatch{}, err
	}
	patch.Labels.Add = in.addLabels
	patch.Labels.Remove = in.removeLabels
	if in.setLabels != nil {
		patch.Labels.Replace = setField(*in.setLabels)
	}
	if in.reparent != nil {
		patch.ParentID = setField(*in.reparent)
	}
	if in.hasAppendNotes {
		patch.AppendNotes = setField(in.appendNotes)
	}
	if len(in.mergeMetadataIn) > 0 {
		patch.Metadata.Merge = setField(in.mergeMetadataIn)
	}
	if len(in.setMetadata) > 0 {
		set, err := parseSetMetadataFlags(in.setMetadata)
		if err != nil {
			return issueops.IssuePatch{}, err
		}
		patch.Metadata.Set = set
	}
	if len(in.unsetMetadata) > 0 {
		patch.Metadata.Unset = in.unsetMetadata
	}
	// GH#3233: --defer="" restores ready visibility only if the issue was
	// actually deferred, exactly as the direct route decides it.
	if in.clearDeferStatus && before.Status == types.StatusDeferred {
		patch.Status = setField(types.StatusOpen)
	}
	return patch, nil
}

// applyUpdateProxiedAttempt runs one full read-merge-write attempt in the fresh
// unit of work handed to it. It returns a serialization failure verbatim so the
// shared retry loop redoes the whole attempt — the server-side rollback
// guarantees nothing landed. Terminal per-issue failures (not found, claim
// conflicts, guard mismatches) print to stderr and come back as a non-nil fail
// with an empty commit message, so nothing is committed, the multi-ID loop
// records the failed ID, keeps going, and still exits non-zero — matching the
// non-proxied path. A guard refusal sets fail.GuardMismatch so the exit code
// distinguishes it (ExitGuardMismatch vs 1).
func applyUpdateProxiedAttempt(ctx context.Context, uw uow.UnitOfWork, id string, in *updateInput) (proxiedUpdateAttempt, string, error) {
	issueUC := uw.IssueUseCase()
	current, err := issueUC.GetIssue(ctx, id)
	if err != nil || current == nil {
		wispCurrent, wispErr := issueUC.GetWisp(ctx, id)
		if wispErr == nil && wispCurrent != nil {
			current = wispCurrent
		} else if err != nil {
			fmt.Fprintf(os.Stderr, "Error resolving %s: %v\n", id, err)
			return failedUpdateAttempt(&updateIDFailure{ID: id, Error: fmt.Sprintf("resolving issue: %v", err)})
		} else {
			fmt.Fprintf(os.Stderr, "Issue %s not found\n", id)
			return failedUpdateAttempt(&updateIDFailure{ID: id, Error: "issue not found"})
		}
	}
	if err := validateIssueUpdatable(id, current); err != nil {
		fmt.Fprintf(os.Stderr, "%s\n", err)
		return failedUpdateAttempt(&updateIDFailure{ID: id, Error: err.Error()})
	}

	// bd-98s5c: an unguarded assignee update must not silently overwrite
	// another actor's live claim. Skipped under --if-assignee: that CAS names
	// the holder explicitly (park stays possible without --force). Also
	// skipped under --claim: the claim CAS is itself the anti-steal gate (a
	// foreign live claim fails it with the canonical "already claimed" copy),
	// and an assignee edit that rides a WON claim only ever touches the
	// actor's own fresh claim. The proxied-server path is where cross-actor
	// collisions actually happen — every shared-dolt-server clone writes
	// through here. A policy refusal: terminal per-issue failure, exit 1,
	// never GuardMismatch/13.
	if newAssignee, ok := in.fields["assignee"].(string); ok && in.ifAssignee == nil && !in.claim {
		if err := validateIssueReassignable(id, current, actor, newAssignee,
			uowClaimPoolAliases(ctx, uw), in.force); err != nil {
			fmt.Fprintf(os.Stderr, "%s\n", err)
			return failedUpdateAttempt(&updateIDFailure{ID: id, Error: err.Error()})
		}
	}

	spec := buildUpdateSpecForIssue(current, in)
	notesOverwritten := replacesExistingNotes(current.Notes, in.fields)

	updated, err := issueUC.ApplyUpdate(ctx, id, spec, actor)
	if err != nil {
		if uow.IsSerializationError(err) {
			return proxiedUpdateAttempt{}, "", err
		}
		if errors.Is(err, storage.ErrAlreadyClaimed) || errors.Is(err, storage.ErrNotClaimable) {
			fmt.Fprintf(os.Stderr, "Error claiming %s: %v\n", id, err)
			return failedUpdateAttempt(&updateIDFailure{ID: id, Error: fmt.Sprintf("claiming issue: %v", err)})
		}
		// Close policy refused the status change. Same copy the proxied close
		// prints for the same two refusals, so the boundary reads identically
		// whichever verb a script reached it through. A policy refusal is a
		// terminal per-issue failure — exit 1, never GuardMismatch/13.
		if errors.Is(err, storage.ErrCloseOpenChildren) {
			fmt.Fprintf(os.Stderr, "%v\n", err)
			return failedUpdateAttempt(&updateIDFailure{ID: id, Error: err.Error()})
		}
		if errors.Is(err, storage.ErrCloseBlocked) {
			fmt.Fprintf(os.Stderr, "%v (use --force to override)\n", err)
			return failedUpdateAttempt(&updateIDFailure{ID: id, Error: fmt.Sprintf("%v (use --force to override)", err)})
		}
		if isGuardMismatch(err) {
			// bd-wsqvw guard verdict: the precondition no longer holds, nothing
			// was written. Loud and non-zero, never collapsed to success —
			// GuardMismatch routes the batch to ExitGuardMismatch.
			fmt.Fprintf(os.Stderr, "Error updating %s: %v\n", id, err)
			return failedUpdateAttempt(&updateIDFailure{ID: id, Error: fmt.Sprintf("precondition failed: %v", err), GuardMismatch: true})
		}
		fmt.Fprintf(os.Stderr, "Error updating %s: %v\n", id, err)
		return failedUpdateAttempt(&updateIDFailure{ID: id, Error: fmt.Sprintf("updating: %v", err)})
	}

	// The commit belongs to RunTxResultWithin: it retries the whole attempt on a
	// serialization failure (Dolt rolled the transaction back server-side, so
	// nothing landed) and tolerates "nothing to commit", the legitimately-empty
	// working set of a wisp-only update — those live in dolt_ignored tables, so a
	// successful ApplyUpdate can leave the Dolt commit layer with nothing to do.
	// The lost-write flavor of nothing-to-commit — re-committing a rolled-back
	// session — cannot arise, because each attempt commits its own fresh unit of
	// work exactly once.
	return proxiedUpdateAttempt{
		issue:            updated,
		before:           current,
		notesOverwritten: notesOverwritten,
	}, fmt.Sprintf("bd: update %s", id), nil
}

// failedUpdateAttempt returns a terminal per-issue failure with no commit
// message, so the shared retry loop skips the commit and stops retrying.
func failedUpdateAttempt(fail *updateIDFailure) (proxiedUpdateAttempt, string, error) {
	return proxiedUpdateAttempt{fail: fail}, "", nil
}

func fireProxiedUpdateHooks(ctx context.Context, before, after *types.Issue) error {
	if after == nil {
		return nil
	}
	runner, err := proxiedHookRunner(ctx)
	if err != nil {
		return fmt.Errorf("hook runner: %w", err)
	}
	if runner == nil {
		return nil
	}
	if err := runner.RunSync(hooks.EventUpdate, after); err != nil {
		return fmt.Errorf("on_update hook: %w", err)
	}
	if before != nil &&
		before.Status != types.StatusClosed &&
		after.Status == types.StatusClosed {
		if err := runner.RunSync(hooks.EventClose, after); err != nil {
			return fmt.Errorf("on_close hook: %w", err)
		}
	}
	return nil
}

func proxiedHookRunner(ctx context.Context) (*hooks.Runner, error) {
	if hookRunner != nil {
		return hookRunner, nil
	}
	cwd, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("getwd: %w", err)
	}
	fsProvider := fs.NewFileSystemProvider(cwd, newBeadsDirTemplates(), newFileSystemAdapters())
	resolution := fsProvider.BeadsDirFSUseCase().ResolveBeadsDir(ctx)
	if resolution.BeadsDir == "" {
		return nil, nil
	}
	return hooks.NewRunner(filepath.Join(resolution.BeadsDir, "hooks")), nil
}

// buildUpdateSpecForIssue translates gathered CLI input into a domain
// UpdateSpec. It never pre-merges row state: merge-shaped edits are passed as
// operation keys and resolved by the repository inside the mutation
// transaction.
func buildUpdateSpecForIssue(current *types.Issue, in *updateInput) domain.UpdateSpec {
	fields := make(map[string]any, len(in.fields))
	for k, v := range in.fields {
		fields[k] = v
	}

	if in.clearDeferStatus && current.Status == types.StatusDeferred {
		fields["status"] = string(types.StatusOpen)
	}
	// Metadata edits and note appends pass through as merge OPERATIONS: the
	// repository resolves them against the row re-read inside the mutation
	// transaction (issueops.ResolveMergeOps via the domain/db Update path).
	// Merging here against `current` — a read from this unit of work's MVCC
	// snapshot — silently erased keys a concurrent writer committed after our
	// snapshot was taken: both processes exited 0, one write vanished.
	if in.hasAppendNotes {
		fields[storageissueops.OpAppendNotes] = in.appendNotes
	}
	if len(in.mergeMetadataIn) > 0 {
		fields[storageissueops.OpMergeMetadata] = in.mergeMetadataIn
	}
	if len(in.setMetadata) > 0 {
		fields[storageissueops.OpSetMetadata] = in.setMetadata
	}
	if len(in.unsetMetadata) > 0 {
		fields[storageissueops.OpUnsetMetadata] = in.unsetMetadata
	}
	// --force means both of its halves here too. The assignee half is applied
	// above by validateIssueReassignable; this is the close-policy half, which
	// the repository pops before it validates fields.
	if in.force {
		fields[storageissueops.OpForceClosePolicy] = true
	}

	return domain.UpdateSpec{
		Fields:           fields,
		Claim:            in.claim,
		AddLabels:        in.addLabels,
		RemoveLabels:     in.removeLabels,
		SetLabels:        in.setLabels,
		Reparent:         in.reparent,
		ExpectedAssignee: in.ifAssignee,
		ExpectedStatus:   in.ifStatus,
	}
}
