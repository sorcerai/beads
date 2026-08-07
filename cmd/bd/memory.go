package main

import (
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/steveyegge/beads/internal/metrics"
	"github.com/steveyegge/beads/internal/storage/kvkeys"
)

// memoryPrefix is prepended (after kvPrefix) to all memory keys.
const memoryPrefix = kvkeys.MemoryPrefix

// memorySupersededPrefix stores supersession edges.
// When memory A is superseded by B: kv.memory-superseded.A = B
const memorySupersededPrefix = "memory-superseded."

// memoryKeyFlag allows explicit key override for bd remember.
var memoryKeyFlag string

// slugify converts a string to a URL-friendly slug for use as a memory key.
// Takes the first ~8 words, lowercases, replaces non-alphanumeric with hyphens.
func slugify(s string) string {
	s = strings.ToLower(s)
	// Replace non-alphanumeric chars with hyphens
	re := regexp.MustCompile(`[^a-z0-9]+`)
	s = re.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")

	// Limit to first ~8 "words" (hyphen-separated segments)
	parts := strings.SplitN(s, "-", 10)
	if len(parts) > 8 {
		parts = parts[:8]
	}
	slug := strings.Join(parts, "-")

	// Cap total length
	if len(slug) > 60 {
		slug = slug[:60]
		// Don't end on a hyphen
		slug = strings.TrimRight(slug, "-")
	}
	return slug
}

// matchesKnownCommand reports whether insight is a single bare word that
// matches the name or an alias of a top-level bd command. It is used to catch
// `bd remember <subcommand>` mistakes before they become accidental memories.
// Multi-word insights (the normal case) always pass, since they contain
// whitespace and so cannot be a single command token.
func matchesKnownCommand(cmd *cobra.Command, insight string) (string, bool) {
	word := strings.TrimSpace(insight)
	if word == "" || strings.ContainsAny(word, " \t\r\n") {
		return "", false
	}
	for _, c := range cmd.Root().Commands() {
		if strings.EqualFold(c.Name(), word) {
			return c.Name(), true
		}
		for _, alias := range c.Aliases {
			if strings.EqualFold(alias, word) {
				return c.Name(), true
			}
		}
	}
	return "", false
}

// rememberBareKeyPath implements the desire-path / footgun guard for
// `bd remember <bare-slug>` (no --key): a bare slug naming an EXISTING memory
// is recalled instead of stored; a bare slug naming nothing is refused.
// Shared by the classic and proxied-server paths. The caller only invokes it
// when memoryKeyFlag == "" and slugify(insight) == insight.
func rememberBareKeyPath(key, insight, existing string) error {
	if existing != "" {
		if jsonOutput {
			return outputJSON(map[string]interface{}{
				"key":    key,
				"value":  existing,
				"found":  true,
				"action": "recalled",
			})
		}
		fmt.Fprintf(os.Stderr,
			"(recalled %q -- a bare existing key READS. To overwrite: `bd remember \"<new content>\" --key %s`)\n",
			key, key)
		fmt.Printf("%s\n", existing)
		return nil
	}
	return HandleErrorRespectJSON(
		"no memory named %q to recall -- and refusing to store a bare key-like token as its own content. "+
			"`bd remember` WRITES (its positional arg is CONTENT, not a key). "+
			"To store it anyway: `bd remember %q --key %s`. To browse keys: `bd memories`",
		key, insight, key)
}

// printRememberResult renders the `bd remember` success output. Shared by
// the classic and proxied-server paths.
func printRememberResult(verb, key, insight string) error {
	if jsonOutput {
		return outputJSON(map[string]string{
			"key":    key,
			"value":  insight,
			"action": strings.ToLower(verb),
		})
	}
	fmt.Printf("%s [%s]: %s\n", verb, key, truncateMemory(insight, 80))
	return nil
}

// memoriesFromConfig filters a full config map down to the kv.memory.*
// namespace (stripping the prefix), optionally filtered by a lowercase
// search term matched against key or value. Shared by the classic and
// proxied-server paths.
func memoriesFromConfig(allConfig map[string]string, search string) map[string]string {
	fullPrefix := kvkeys.MemoryConfigKeyPrefix
	memories := make(map[string]string)
	for k, v := range allConfig {
		if strings.HasPrefix(k, fullPrefix) {
			userKey := strings.TrimPrefix(k, fullPrefix)
			memories[userKey] = v
		}
	}
	if search != "" {
		filtered := make(map[string]string)
		for k, v := range memories {
			if strings.Contains(strings.ToLower(k), search) ||
				strings.Contains(strings.ToLower(v), search) {
				filtered[k] = v
			}
		}
		memories = filtered
	}
	return memories
}

// printMemoriesResult renders the `bd memories` output. Shared by the
// classic and proxied-server paths.
func printMemoriesResult(memories map[string]string, search string, supersededBy map[string]string) error {
	// supersededBy is nil for callers without a direct store (proxied server,
	// prime): they get upstream's plain rendering, unchanged.
	if supersededBy != nil && !memoriesAllFlag {
		visible := make(map[string]string, len(memories))
		for k, v := range memories {
			if _, ok := supersededBy[k]; !ok {
				visible[k] = v
			}
		}
		memories = visible
	}

	if jsonOutput {
		if supersededBy == nil {
			return outputJSON(memories)
		}
		out := make(map[string]interface{}, len(memories))
		for k, v := range memories {
			record := map[string]interface{}{"key": k, "value": v}
			if replacement, ok := supersededBy[k]; ok {
				record["superseded_by"] = replacement
			}
			out[k] = record
		}
		return outputJSON(out)
	}

	if len(memories) == 0 {
		if search != "" {
			fmt.Printf("No memories matching %q\n", search)
		} else {
			fmt.Println("No memories stored. Use 'bd remember \"insight\"' to add one.")
		}
		return nil
	}

	keys := make([]string, 0, len(memories))
	for k := range memories {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	if search != "" {
		fmt.Printf("Memories matching %q:\n\n", search)
	} else {
		fmt.Printf("Memories (%d):\n\n", len(memories))
	}
	for _, k := range keys {
		v := memories[k]
		if replacement, ok := supersededBy[k]; ok {
			fmt.Printf("  %s  \u2190 superseded by %s\n", k, replacement)
		} else {
			fmt.Printf("  %s\n", k)
		}
		fmt.Printf("    %s\n\n", truncateMemory(v, 120))
	}
	return nil
}

// printForgetNotFound renders the `bd forget` missing-key output (including
// the SilentExit contract). Shared by the classic and proxied-server paths.
func printForgetNotFound(key string) error {
	if jsonOutput {
		if jerr := outputJSON(map[string]string{
			"key":   key,
			"found": "false",
		}); jerr != nil {
			return jerr
		}
		return SilentExit()
	}
	fmt.Fprintf(os.Stderr, "No memory with key %q\n", key)
	return SilentExit()
}

// printForgetResult renders the `bd forget` success output. Shared by the
// classic and proxied-server paths.
func printForgetResult(key, existing string) error {
	if jsonOutput {
		return outputJSON(map[string]string{
			"key":     key,
			"deleted": "true",
		})
	}
	fmt.Printf("Forgot [%s]: %s\n", key, truncateMemory(existing, 80))
	return nil
}

// printRecallResult renders the `bd recall` output (including the not-found
// SilentExit contract). Shared by the classic and proxied-server paths.
func printRecallResult(key, value, supersededBy string) error {
	if jsonOutput {
		result := map[string]interface{}{
			"key":   key,
			"value": value,
			"found": value != "",
		}
		if supersededBy != "" {
			result["superseded_by"] = supersededBy
		}
		if jerr := outputJSON(result); jerr != nil {
			return jerr
		}
		if value == "" {
			return SilentExit()
		}
		return nil
	}
	if value == "" {
		fmt.Fprintf(os.Stderr, "No memory with key %q\n", key)
		return SilentExit()
	}
	if supersededBy != "" {
		fmt.Printf("[SUPERSEDED by %s]\n%s\n", supersededBy, value)
		return nil
	}
	fmt.Printf("%s\n", value)
	return nil
}

// rememberCmd stores a memory.
var rememberCmd = &cobra.Command{
	Use:   `remember "<insight>"`,
	Short: "Store a persistent memory",
	Long: `Store a memory that persists across sessions and account rotations.

Memories are injected at prime time (bd prime) so you have them
in every session without manual loading.

The positional arg is the memory CONTENT (the key is auto-generated from it
unless --key is given). As a convenience, if the arg is a bare key naming an
existing memory, it is RECALLED instead of stored (same as 'bd recall');
a bare key naming nothing is refused. Use --key to store slug-like content.

Examples:
  bd remember "always run tests with -race flag"
  bd remember "Dolt phantom DBs hide in three places" --key dolt-phantoms
  bd remember "auth module uses JWT not sessions" --key auth-jwt
  bd remember dolt-phantoms        # bare existing key: reads it (= bd recall)`,
	GroupID:       "setup",
	Args:          cobra.ExactArgs(1),
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		CheckReadonly("remember")

		evt := metrics.NewCommandEvent("remember")
		defer func() {
			if c := metrics.Global(); c != nil {
				c.CloseEventAndAdd(evt)
			}
		}()

		insight := args[0]
		if strings.TrimSpace(insight) == "" {
			return HandleErrorRespectJSON("memory content cannot be empty")
		}

		// Guard against a subcommand-like first argument being silently stored
		// as memory content. `bd remember` is a leaf command, so a mistaken
		// `bd remember recall` (or any bare bd command name) would otherwise
		// store the word "recall" as a memory instead of doing what the user
		// intended (GH#4401). A genuine insight is a phrase, so only a single
		// bare word that matches a known command is treated as suspect, and an
		// explicit --key signals deliberate intent and bypasses the guard.
		if memoryKeyFlag == "" {
			if name, ok := matchesKnownCommand(cmd, insight); ok {
				return HandleErrorWithHintRespectJSON(
					fmt.Sprintf("%q looks like a command, not something to remember", insight),
					fmt.Sprintf("Did you mean 'bd %s'? To store %q as a memory anyway, give it an explicit key: bd remember %q --key <key>", name, insight, insight),
				)
			}
		}

		// Generate or use provided key
		key := memoryKeyFlag
		if key == "" {
			key = slugify(insight)
		}
		if key == "" {
			return HandleErrorRespectJSON("could not generate key from content; use --key to specify one")
		}

		if usesProxiedServer() {
			return runRememberProxiedServer(rootCtx, key, insight)
		}

		if err := ensureDirectMode("remember requires direct database access"); err != nil {
			return HandleError("%v", err)
		}

		storageKey := kvPrefix + memoryPrefix + key

		ctx := rootCtx

		existing, _ := store.GetConfig(ctx, storageKey)
		verb := "Remembered"
		if existing != "" {
			verb = "Updated"
		}

		// Desire path + footgun guard: `bd remember <x>` is a WRITE whose positional arg is
		// the CONTENT, not a key -- but "remember X" reads as a getter in English, so agents
		// routinely type `bd remember some-key` meaning "do you remember X?". The tell-tale of
		// a mistyped read is content that round-trips through slugify unchanged (a bare slug);
		// real prose insights never do. When that happens and no explicit --key was given:
		//   - the key EXISTS  -> pave the desire path: recall it instead of writing
		//   - no such key     -> refuse; storing a key-like token as its own content would
		//                        create a junk memory that hides the mistake
		// Passing --key states write intent and bypasses both branches.
		if memoryKeyFlag == "" && slugify(insight) == insight {
			return rememberBareKeyPath(key, insight, existing)
		}

		if err := store.SetConfig(ctx, storageKey, insight); err != nil {
			return HandleErrorRespectJSON("storing memory: %v", err)
		}
		commandDidWrite.Store(true)

		return printRememberResult(verb, key, insight)
	},
}

var memoriesAllFlag bool

// memoriesCmd lists and searches memories.
var memoriesCmd = &cobra.Command{
	Use:   "memories [search]",
	Short: "List or search persistent memories",
	Long: `List all memories, or search by keyword.

Superseded memories are hidden by default; use --all to include them.

Examples:
  bd memories              # list active memories
  bd memories dolt         # search for memories about dolt
  bd memories "race flag"  # search for a phrase
  bd memories --all        # include superseded memories`,
	GroupID:       "setup",
	Args:          cobra.MaximumNArgs(1),
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		evt := metrics.NewCommandEvent("memories")
		defer func() {
			if c := metrics.Global(); c != nil {
				c.CloseEventAndAdd(evt)
			}
		}()

		var search string
		if len(args) > 0 {
			search = strings.ToLower(args[0])
		}

		if usesProxiedServer() {
			return runMemoriesProxiedServer(rootCtx, search)
		}

		if err := ensureDirectMode("memories requires direct database access"); err != nil {
			return HandleError("%v", err)
		}

		ctx := rootCtx
		allConfig, err := store.GetAllConfig(ctx)
		if err != nil {
			return HandleErrorRespectJSON("listing memories: %v", err)
		}

		return printMemoriesResult(memoriesFromConfig(allConfig, search), search, getSupersededMap())
	},
}

// forgetCmd removes a memory.
var forgetCmd = &cobra.Command{
	Use:   "forget <key>",
	Short: "Remove a persistent memory",
	Long: `Remove a memory by its key.

Use 'bd memories' to see available keys.

Examples:
  bd forget dolt-phantoms
  bd forget auth-jwt`,
	GroupID:       "setup",
	Args:          cobra.ExactArgs(1),
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		CheckReadonly("forget")

		evt := metrics.NewCommandEvent("forget")
		defer func() {
			if c := metrics.Global(); c != nil {
				c.CloseEventAndAdd(evt)
			}
		}()

		key := args[0]

		if usesProxiedServer() {
			return runForgetProxiedServer(rootCtx, key)
		}

		if err := ensureDirectMode("forget requires direct database access"); err != nil {
			return HandleError("%v", err)
		}

		storageKey := kvPrefix + memoryPrefix + key

		ctx := rootCtx

		existing, _ := store.GetConfig(ctx, storageKey)
		if existing == "" {
			return printForgetNotFound(key)
		}

		if err := store.DeleteConfig(ctx, storageKey); err != nil {
			return HandleErrorRespectJSON("forgetting memory: %v", err)
		}
		commandDidWrite.Store(true)

		return printForgetResult(key, existing)
	},
}

// recallCmd retrieves a specific memory by key.
var recallCmd = &cobra.Command{
	Use:   "recall <key>",
	Short: "Retrieve a specific memory",
	Long: `Retrieve the full content of a memory by its key.

Examples:
  bd recall dolt-phantoms
  bd recall auth-jwt`,
	GroupID:       "setup",
	Args:          cobra.ExactArgs(1),
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		evt := metrics.NewCommandEvent("recall")
		defer func() {
			if c := metrics.Global(); c != nil {
				c.CloseEventAndAdd(evt)
			}
		}()

		key := args[0]

		if usesProxiedServer() {
			return runRecallProxiedServer(rootCtx, key)
		}

		if err := ensureDirectMode("recall requires direct database access"); err != nil {
			return HandleError("%v", err)
		}

		storageKey := kvPrefix + memoryPrefix + key

		ctx := rootCtx
		value, err := store.GetConfig(ctx, storageKey)
		if err != nil {
			return HandleErrorRespectJSON("recalling memory: %v", err)
		}

		return printRecallResult(key, value, isSuperseded(key))
	},
}

// truncateMemory shortens a string to maxLen for display.
func truncateMemory(s string, maxLen int) string {
	// Replace newlines with spaces for single-line display
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}

// isSuperseded checks if a memory key has been superseded.
// Returns the replacement key if superseded, empty string otherwise.
func isSuperseded(key string) string {
	storageKey := kvPrefix + memorySupersededPrefix + key
	ctx := rootCtx
	value, err := store.GetConfig(ctx, storageKey)
	if err != nil || value == "" {
		return ""
	}
	return value
}

// getSupersededMap returns a map of superseded key -> replacement key.
func getSupersededMap() map[string]string {
	ctx := rootCtx
	allConfig, err := store.GetAllConfig(ctx)
	if err != nil {
		return nil
	}
	fullPrefix := kvPrefix + memorySupersededPrefix
	m := make(map[string]string)
	for k, v := range allConfig {
		if strings.HasPrefix(k, fullPrefix) {
			userKey := strings.TrimPrefix(k, fullPrefix)
			m[userKey] = v
		}
	}
	return m
}

// memoryCmd is the parent command for memory subcommands.
var memoryCmd = &cobra.Command{
	Use:     "memory",
	Short:   "Memory management commands",
	Long:    `Commands for managing persistent memories.`,
	GroupID: "setup",
	Run: func(cmd *cobra.Command, args []string) {
		_ = cmd.Help()
	},
}

// memorySupersedeCmd marks a memory as superseded by another.
var memorySupersedeCmd = &cobra.Command{
	Use:   "supersede <old-key> --with=<new-key>",
	Short: "Supersede a memory with a newer version",
	Long: `Mark a memory as superseded by a newer memory.

The superseded memory is hidden from default listings (bd memories)
but remains recoverable via 'bd recall' and 'bd memories --all'.

This is safer than 'bd forget' because the old reasoning is preserved.

Examples:
  bd memory supersede auth-old --with auth-new
  bd memory supersede deploy-notes-v1 --with deploy-notes-v2`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		CheckReadonly("memory supersede")

		if err := ensureDirectMode("memory supersede requires direct database access"); err != nil {
			return HandleErrorRespectJSON("%v", err)
		}

		oldKey := args[0]
		newKey := memorySupersedeWithFlag

		if newKey == "" {
			return HandleErrorRespectJSON("--with flag is required: bd memory supersede <old-key> --with=<new-key>")
		}

		ctx := rootCtx

		// Verify old memory exists
		oldStorageKey := kvPrefix + memoryPrefix + oldKey
		oldValue, err := store.GetConfig(ctx, oldStorageKey)
		if err != nil {
			return HandleErrorRespectJSON("reading memory %q: %v", oldKey, err)
		}
		if oldValue == "" {
			return HandleErrorRespectJSON("no memory with key %q", oldKey)
		}

		// Verify new memory exists
		newStorageKey := kvPrefix + memoryPrefix + newKey
		newValue, err := store.GetConfig(ctx, newStorageKey)
		if err != nil {
			return HandleErrorRespectJSON("reading memory %q: %v", newKey, err)
		}
		if newValue == "" {
			return HandleErrorRespectJSON("no memory with key %q (use --with to specify an existing memory)", newKey)
		}

		// Store the supersession edge
		superStorageKey := kvPrefix + memorySupersededPrefix + oldKey
		if err := store.SetConfig(ctx, superStorageKey, newKey); err != nil {
			return HandleErrorRespectJSON("storing supersession: %v", err)
		}
		commandDidWrite.Store(true)

		if jsonOutput {
			return outputJSON(map[string]string{
				"action":        "superseded",
				"key":           oldKey,
				"superseded_by": newKey,
			})
		}

		fmt.Printf("Superseded [%s] -> [%s]\n", oldKey, newKey)
		return nil
	},
}

var memorySupersedeWithFlag string

func init() {
	rememberCmd.Flags().StringVar(&memoryKeyFlag, "key", "", "Explicit key for the memory (auto-generated from content if not set). If a memory with this key already exists, it will be updated in place")
	memoriesCmd.Flags().BoolVar(&memoriesAllFlag, "all", false, "Include superseded memories")

	rootCmd.AddCommand(rememberCmd)
	rootCmd.AddCommand(memoriesCmd)
	rootCmd.AddCommand(forgetCmd)
	rootCmd.AddCommand(recallCmd)

	memorySupersedeCmd.Flags().StringVar(&memorySupersedeWithFlag, "with", "", "Replacement memory key (required)")
	memoryCmd.AddCommand(memorySupersedeCmd)
	rootCmd.AddCommand(memoryCmd)
}
