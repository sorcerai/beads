package main

import (
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/steveyegge/beads/internal/memoryapi"
	"github.com/steveyegge/beads/internal/metrics"
	"github.com/steveyegge/beads/internal/storage/kvkeys"
	"github.com/steveyegge/beads/internal/storage/uow"
	"github.com/steveyegge/beads/memoryops"
)

// openMemories hands back the persistent-memory role for whichever route this
// invocation is on, each through its OWN capability accessor — the store's for
// the direct route and the provider's for the proxied one.
//
// directRequirement is the message `ensureDirectMode` reports when a workspace
// is reachable by neither route. It is per-verb because the shipped text names
// the verb.
func openMemories(directRequirement string) (memoryops.Memories, error) {
	if usesProxiedServer() {
		return proxiedMemories()
	}
	if err := ensureDirectMode(directRequirement); err != nil {
		return nil, err
	}
	return store.Memories()
}

// proxiedMemories hands back the guarded persistent-memory surface for this
// invocation's proxied-server provider, through the provider's OWN capability
// accessor — the same two-step proxiedWorkspaceConfig performs.
func proxiedMemories() (memoryops.Memories, error) {
	if uowProvider == nil {
		return nil, errors.New("proxied-server UOW provider not initialized")
	}
	return memoriesFromProvider(uowProvider)
}

// memoriesFromProvider is that accessor step for a provider the caller names.
//
// It takes the provider rather than reading the global one because `bd prime`
// opens a provider SCOPED to its read — prime is in noDbCommands, so the root
// pre-run opens nothing — and one spelling of "ask this provider for the memory
// surface" is the whole point of having an accessor at all.
func memoriesFromProvider(provider uow.UnitOfWorkProvider) (memoryops.Memories, error) {
	src, ok := provider.(uow.MemoriesSource)
	if !ok {
		return nil, fmt.Errorf("proxied-server provider %T does not offer the persistent-memory surface", provider)
	}
	return src.Memories()
}

// noteDirectMemoryWrite marks the invocation as having written, which is what
// the auto-commit epilogue in main.go keys on.
//
// It is DIRECT-ROUTE ONLY, and both halves of that matter. A direct memory
// write lands in the Dolt working set and nothing else commits it, so a verb
// that forgets to call this stores a memory that exists until the process exits
// and then sits uncommitted — visible to the session that wrote it and to
// nothing after. A proxied write already committed inside the role's unit of
// work, so flagging it there would ask the epilogue to commit a second time on
// a route with nothing outstanding.
//
// The RunEs cannot make that distinction themselves: openMemories hides which
// route they are on, which is the point. So the guard lives here, once, the way
// noteDirectConfigWrite does for the settings plane.
func noteDirectMemoryWrite() {
	if !usesProxiedServer() {
		commandDidWrite.Store(true)
	}
}

// memoryPrefix is prepended (after kvPrefix) to all memory keys.
const memoryPrefix = kvkeys.MemoryPrefix

// memorySupersededPrefix stores supersession edges.
// When memory A is superseded by B: kv.memory-superseded.A = B
const memorySupersededPrefix = "memory-superseded."

// memoryKeyFlag allows explicit key override for bd remember.
var memoryKeyFlag string

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
// is recalled instead of stored; a bare slug naming nothing is refused. The
// caller only invokes it when memoryKeyFlag == "" and the insight round-trips
// through memoryapi.DeriveKey unchanged, having already read the key.
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

// printRememberResult renders the `bd remember` success output.
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

// printMemoriesResult renders the `bd memories` output.
func printMemoriesResult(memories map[string]string, search string) error {
	// Supersede support: superseded memories are hidden unless --all, and
	// annotated with their replacement when shown.
	supersededMap := getSupersededMap()

	if jsonOutput {
		outputMap := make(map[string]interface{})
		for k, v := range memories {
			if !memoriesAllFlag {
				if _, ok := supersededMap[k]; ok {
					continue
				}
			}
			record := map[string]interface{}{
				"key":   k,
				"value": v,
			}
			if replacement, ok := supersededMap[k]; ok {
				record["superseded_by"] = replacement
			}
			outputMap[k] = record
		}
		return outputJSON(outputMap)
	}

	// Text output: filter out superseded unless --all
	if !memoriesAllFlag {
		visible := make(map[string]string)
		for k, v := range memories {
			if _, ok := supersededMap[k]; !ok {
				visible[k] = v
			}
		}
		memories = visible
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
		if replacement, ok := supersededMap[k]; ok {
			fmt.Printf("  %s  ← superseded by %s\n", k, replacement)
		} else {
			fmt.Printf("  %s\n", k)
		}
		// Indent the value, wrapping long lines
		fmt.Printf("    %s\n\n", truncateMemory(v, 120))
	}
	return nil
}

// printForgetNotFound renders the `bd forget` missing-key output (including
// the SilentExit contract).
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

// printForgetResult renders the `bd forget` success output.
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
// SilentExit contract).
func printRecallResult(key, value string) error {
	replacement := isSuperseded(key)
	if jsonOutput {
		result := map[string]interface{}{
			"key":   key,
			"value": value,
			"found": value != "",
		}
		if replacement != "" {
			result["superseded_by"] = replacement
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
	if replacement != "" {
		fmt.Printf("[SUPERSEDED by %s]\n%s\n", replacement, value)
	} else {
		fmt.Printf("%s\n", value)
	}
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

		// Guard against a subcommand-like first argument being silently stored
		// as memory content. `bd remember` is a leaf command, so a mistaken
		// `bd remember recall` (or any bare bd command name) would otherwise
		// store the word "recall" as a memory instead of doing what the user
		// intended (GH#4401). A genuine insight is a phrase, so only a single
		// bare word that matches a known command is treated as suspect, and an
		// explicit --key signals deliberate intent and bypasses the guard.
		//
		// It stays at the FRONT DOOR and stays FIRST: it reads the cobra command
		// tree, which no role can see, and it must answer before any storage is
		// opened so that `bd remember list` in a directory with no workspace
		// still says "looks like a command".
		if memoryKeyFlag == "" {
			if name, ok := matchesKnownCommand(cmd, insight); ok {
				return HandleErrorWithHintRespectJSON(
					fmt.Sprintf("%q looks like a command, not something to remember", insight),
					fmt.Sprintf("Did you mean 'bd %s'? To store %q as a memory anyway, give it an explicit key: bd remember %q --key <key>", name, insight, insight),
				)
			}
		}

		memories, err := openMemories("remember requires direct database access")
		if err != nil {
			return HandleError("%v", err)
		}

		// Desire path + footgun guard: `bd remember <x>` is a WRITE whose positional arg is
		// the CONTENT, not a key -- but "remember X" reads as a getter in English, so agents
		// routinely type `bd remember some-key` meaning "do you remember X?". The tell-tale of
		// a mistyped read is content that round-trips through the key derivation unchanged (a
		// bare slug); real prose insights never do. When that happens and no explicit --key was
		// given:
		//   - the key EXISTS  -> pave the desire path: recall it instead of writing
		//   - no such key     -> refuse; storing a key-like token as its own content would
		//                        create a junk memory that hides the mistake
		// Passing --key states write intent and bypasses both branches.
		//
		// It stays ABOVE the role because it decides WHETHER TO WRITE AT ALL, and
		// because it exists to disambiguate English: an HTTP POST is not ambiguous
		// and must not inherit it. The read below is a plain Recall, so this whole
		// branch touches nothing.
		//
		// `derived != ""` is load-bearing and is not decoration: DeriveKey("")
		// is "", so without it every empty or unslugifiable insight would satisfy
		// derived == insight and be routed into a "recall" of the empty key. The
		// shipped code was saved from that by an empty-content check that ran
		// first; that check is the role's now, so the condition has to say it.
		derived := memoryapi.DeriveKey(insight)
		if memoryKeyFlag == "" && derived != "" && derived == insight {
			recalled, err := memories.Recall(rootCtx, memoryops.RecallRequest{Key: derived})
			if err != nil {
				return HandleErrorRespectJSON("recalling memory: %v", err)
			}
			return rememberBareKeyPath(derived, insight, recalled.Value)
		}

		result, err := memories.Remember(rootCtx, memoryops.RememberRequest{Key: memoryKeyFlag, Content: insight})
		if err != nil {
			// The role's two refusals ARE this command's shipped sentences —
			// "memory content cannot be empty" and "could not generate key from
			// content; use --key to specify one" — so they print as themselves.
			// Wrapping would reword output an agent may be matching on into
			// "storing memory: validation failed: ..." to say the same thing.
			if errors.Is(err, memoryops.ErrValidation) {
				return HandleErrorRespectJSON("%s", strings.TrimPrefix(err.Error(), memoryops.ErrValidation.Error()+": "))
			}
			return HandleErrorRespectJSON("storing memory: %v", err)
		}
		noteDirectMemoryWrite()

		// Remembered versus Updated is Replaced, observed in the SAME
		// transaction as the write. The shipped code read the row first and
		// described a moment that had already passed.
		verb := "Remembered"
		if result.Replaced {
			verb = "Updated"
		}
		return printRememberResult(verb, result.Key, result.Value)
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
			search = args[0]
		}

		memories, err := openMemories("memories requires direct database access")
		if err != nil {
			return HandleError("%v", err)
		}
		// The term goes to the role RAW. Case folding is List's, so the two
		// routes cannot come to disagree about what matches — which is the
		// whole reason the filter moved down.
		result, err := memories.List(rootCtx, memoryops.ListRequest{Search: search})
		if err != nil {
			return HandleErrorRespectJSON("listing memories: %v", err)
		}

		// The ECHO, on the other hand, has always been lowercased: `bd memories
		// FOO` prints `No memories matching "foo"`. It is a wart — a front door
		// should say back what the user typed — but it is shipped output, and
		// this commit is a convergence, not a change. Fixing it is a one-liner
		// with its own test, like truncateMemory's rune splitting.
		return printMemoriesResult(result.Memories, strings.ToLower(search))
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

		memories, err := openMemories("forget requires direct database access")
		if err != nil {
			return HandleError("%v", err)
		}
		// No pre-read here, deliberately: the value printed below is the one
		// the role's transaction actually deleted, not the one an earlier read
		// happened to see.
		result, err := memories.Forget(rootCtx, memoryops.ForgetRequest{Key: args[0]})
		if err != nil {
			return HandleErrorRespectJSON("forgetting memory: %v", err)
		}
		if !result.Found {
			return printForgetNotFound(result.Key)
		}
		noteDirectMemoryWrite()

		return printForgetResult(result.Key, result.Value)
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

		memories, err := openMemories("recall requires direct database access")
		if err != nil {
			return HandleError("%v", err)
		}
		result, err := memories.Recall(rootCtx, memoryops.RecallRequest{Key: args[0]})
		if err != nil {
			return HandleErrorRespectJSON("recalling memory: %v", err)
		}

		return printRecallResult(result.Key, result.Value)
	},
}

// truncateMemory shortens a string to maxLen for display.
func truncateMemory(s string, maxLen int) string {
	// Replace newlines with spaces for single-line display
	s = strings.ReplaceAll(s, "\n", " ")
	return truncate(s, maxLen)
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
			return HandleError("%v", err)
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
			outputJSON(map[string]string{
				"action":        "superseded",
				"key":           oldKey,
				"superseded_by": newKey,
			})
		} else {
			fmt.Printf("Superseded [%s] -> [%s]\n", oldKey, newKey)
		}
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
