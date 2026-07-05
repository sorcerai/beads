package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/steveyegge/beads/internal/debug"
)

// docsRegenPromptByteCap bounds the inbox content folded into the regen
// prompt (newest entries kept, oldest trimmed) so a huge backlog doesn't
// blow the context window handed to the resident agent.
const docsRegenPromptByteCap = 200 * 1024

// docsRegenCmd is Tier 2: an LLM pass over the inbox + repo that regenerates
// narrative wiki pages. No flags prints the prompt for the resident agent;
// --complete consumes the inbox after the agent (or a human) finished;
// --exec runs a headless CLI with the prompt and completes on success.
var docsRegenCmd = &cobra.Command{
	Use:   "regen",
	Short: "Tier 2: LLM regen of narrative wiki pages from the inbox",
	Args:  cobra.NoArgs,
	Run: func(cmd *cobra.Command, args []string) {
		repoRoot := findRepoRootForArch()
		if repoRoot == "" {
			FatalErrorRespectJSON("not in a git repository")
		}
		docsDir := docsDirName()
		complete, _ := cmd.Flags().GetBool("complete")
		execCLI, _ := cmd.Flags().GetString("exec")

		switch {
		case complete:
			if err := runDocsRegenComplete(repoRoot, docsDir); err != nil {
				FatalErrorRespectJSON("bd docs regen --complete: %v", err)
			}
			fmt.Println("bd docs regen: inbox consumed, watermark advanced")
		case execCLI != "":
			if err := runDocsRegenExec(repoRoot, docsDir, execCLI); err != nil {
				FatalErrorRespectJSON("bd docs regen --exec %s: %v", execCLI, err)
			}
		default:
			fmt.Print(buildDocsRegenPrompt(repoRoot, docsDir))
		}
	},
}

// docsRegenExecArgPrefix maps a headless CLI name to the flags it needs
// before the prompt argument. Unknown names get the prompt as a bare
// positional arg. NEVER add "--model" for agy: broken in print mode (returns
// a persona greeting instead of running the prompt — recorded gotcha).
var docsRegenExecArgPrefix = map[string][]string{
	"claude": {"-p"},
	"agy":    {"-p"},
	"pi":     {"-p"},
	"codex":  {"exec"},
}

// runDocsRegenExec spawns cli with the regen prompt as its final argument,
// BD_DOCS_RUNNING=1 set (reentrancy guard: if the headless run itself closes
// issues, its own post-close hook must not recurse into another regen). On
// exit 0 it consumes the inbox via runDocsRegenComplete; on failure, state is
// left untouched so a retry sees the same inbox.
func runDocsRegenExec(repoRoot, docsDir, cli string) error {
	prompt := buildDocsRegenPrompt(repoRoot, docsDir)
	args := append(append([]string{}, docsRegenExecArgPrefix[cli]...), prompt)
	cmd := exec.Command(cli, args...) // #nosec G204 -- cli is an operator-supplied trusted tool name (--exec flag).
	cmd.Dir = repoRoot
	cmd.Env = append(os.Environ(), "BD_DOCS_RUNNING=1")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s exited non-zero, inbox left untouched: %w", cli, err)
	}
	if err := runDocsRegenComplete(repoRoot, docsDir); err != nil {
		return err
	}
	fmt.Println("bd docs regen --exec: inbox consumed, watermark advanced")
	return nil
}

// buildDocsRegenPrompt is the testable core of the no-flags mode: the prompt
// handed to the resident agent (or fed to --exec).
func buildDocsRegenPrompt(repoRoot, docsDir string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "You are regenerating the living wiki for the repo at %s (docs dir: %s).\n\n", repoRoot, docsDir)
	b.WriteString("Work through the repo and the inbox entries below. Update existing pages in place; do not rewrite unchanged pages.\n")
	b.WriteString("ARCH.md is the source of truth for invariants — link to it, never duplicate it.\n")
	b.WriteString("Cite only real file paths.\n\n")
	b.WriteString("Pages to maintain:\n")
	b.WriteString("  - README.md (index)\n")
	b.WriteString("  - architecture.md\n")
	b.WriteString("  - components/*.md\n\n")

	content, truncated := docsInboxPromptContent(repoRoot, docsDir)
	if truncated {
		b.WriteString("(inbox exceeds 200KB — showing the newest entries only, oldest trimmed)\n\n")
	}
	b.WriteString("Inbox entries:\n\n")
	b.WriteString(content)

	b.WriteString("\nWhen the pages are updated, run `bd docs regen --complete` to consume the inbox and advance the watermark.\n")
	return b.String()
}

// docsInboxPromptContent concatenates every log/ entry (name + contents),
// oldest first, bounded to the newest docsRegenPromptByteCap bytes. Errors
// degrade to an empty inbox — the prompt still says what it says, just short
// on entries.
func docsInboxPromptContent(repoRoot, docsDir string) (content string, truncated bool) {
	logDir := filepath.Join(repoRoot, docsDir, "log")
	dirEntries, err := os.ReadDir(logDir)
	if err != nil {
		return "", false
	}

	names := make([]string, 0, len(dirEntries))
	for _, de := range dirEntries {
		name := de.Name()
		if de.IsDir() || name == "backlog.md" || !strings.HasSuffix(name, ".md") {
			continue
		}
		names = append(names, name)
	}
	sort.Strings(names)

	var parts []string
	for _, name := range names {
		data, err := os.ReadFile(filepath.Join(logDir, name)) // #nosec G304 -- name comes from ReadDir over logDir, not user input.
		if err != nil {
			continue
		}
		parts = append(parts, fmt.Sprintf("--- %s ---\n%s", name, data))
	}
	all := strings.Join(parts, "\n")
	if len(all) <= docsRegenPromptByteCap {
		return all, false
	}
	return all[len(all)-docsRegenPromptByteCap:], true
}

// runDocsRegenComplete consumes every due inbox entry (closed <= now — in
// practice all of them) and advances the watermark + resets the dirty
// counter. Refuses if the repo isn't opted in.
func runDocsRegenComplete(repoRoot, docsDir string) error {
	statePath := docsStatePath(repoRoot, docsDir)
	if _, ok := readDocsState(statePath); !ok {
		return fmt.Errorf("not opted in (run 'bd docs init')")
	}

	now := time.Now().UTC()
	logDir := filepath.Join(repoRoot, docsDir, "log")
	dirEntries, err := os.ReadDir(logDir)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("readdir %s: %w", logDir, err)
	}
	for _, de := range dirEntries {
		name := de.Name()
		if de.IsDir() || name == "backlog.md" || !strings.HasSuffix(name, ".md") {
			continue
		}
		path := filepath.Join(logDir, name)
		data, err := os.ReadFile(path) // #nosec G304 -- path is under <docsDir>/log/, just listed by ReadDir.
		if err != nil {
			continue
		}
		_, _, closed := parseDocsEntryHeader(string(data))
		if closed.After(now) {
			continue // not yet due — leave for the next regen
		}
		if err := os.Remove(path); err != nil {
			debug.Logf("docs regen --complete: remove %s: %v\n", name, err)
		}
	}

	return writeDocsState(statePath, docsState{RegenWatermark: now, Dirty: 0})
}

func init() {
	docsRegenCmd.Flags().Bool("complete", false, "Consume the inbox and advance the regen watermark")
	docsRegenCmd.Flags().String("exec", "", "Run the regen prompt through <cli> headlessly, then --complete on success")
	docsRegenCmd.MarkFlagsMutuallyExclusive("complete", "exec")
	docsCmd.AddCommand(docsRegenCmd)
}
