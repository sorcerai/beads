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

// rememberCmd stores a memory.
var rememberCmd = &cobra.Command{
	Use:   `remember "<insight>"`,
	Short: "Store a persistent memory",
	Long: `Store a memory that persists across sessions and account rotations.

Memories are injected at prime time (bd prime) so you have them
in every session without manual loading.

Examples:
  bd remember "always run tests with -race flag"
  bd remember "Dolt phantom DBs hide in three places" --key dolt-phantoms
  bd remember "auth module uses JWT not sessions" --key auth-jwt`,
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

		if err := ensureDirectMode("remember requires direct database access"); err != nil {
			return HandleError("%v", err)
		}

		insight := args[0]
		if strings.TrimSpace(insight) == "" {
			return HandleErrorRespectJSON("memory content cannot be empty")
		}

		key := memoryKeyFlag
		if key == "" {
			key = slugify(insight)
		}
		if key == "" {
			return HandleErrorRespectJSON("could not generate key from content; use --key to specify one")
		}

		storageKey := kvPrefix + memoryPrefix + key

		ctx := rootCtx

		existing, _ := store.GetConfig(ctx, storageKey)
		verb := "Remembered"
		if existing != "" {
			verb = "Updated"
		}

		if err := store.SetConfig(ctx, storageKey, insight); err != nil {
			return HandleErrorRespectJSON("storing memory: %v", err)
		}
		commandDidWrite.Store(true)

		if jsonOutput {
			return outputJSON(map[string]string{
				"key":    key,
				"value":  insight,
				"action": strings.ToLower(verb),
			})
		}
		fmt.Printf("%s [%s]: %s\n", verb, key, truncateMemory(insight, 80))
		return nil
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

		if err := ensureDirectMode("memories requires direct database access"); err != nil {
			return HandleError("%v", err)
		}

		ctx := rootCtx
		allConfig, err := store.GetAllConfig(ctx)
		if err != nil {
			return HandleErrorRespectJSON("listing memories: %v", err)
		}

		// Build superseded map for filtering
		supersededMap := getSupersededMap()

		// Filter for kv.memory.* keys
		fullPrefix := kvkeys.MemoryConfigKeyPrefix
		memories := make(map[string]string)
		memSupersededBy := make(map[string]string)
		for k, v := range allConfig {
			if strings.HasPrefix(k, fullPrefix) {
				userKey := strings.TrimPrefix(k, fullPrefix)
				memories[userKey] = v
				if replacement, ok := supersededMap[userKey]; ok {
					memSupersededBy[userKey] = replacement
				}
			}
		}

		var search string
		if len(args) > 0 {
			search = strings.ToLower(args[0])
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

		if jsonOutput {
			// JSON output: include all memories (or filtered by search).
			// For --all, always include superseded. Without --all, filter them out.
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
				if replacement, ok := memSupersededBy[k]; ok {
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
			if replacement, ok := memSupersededBy[k]; ok {
				fmt.Printf("  %s  ← superseded by %s\n", k, replacement)
			} else {
				fmt.Printf("  %s\n", k)
			}
			// Indent the value, wrapping long lines
			fmt.Printf("    %s\n\n", truncateMemory(v, 120))
		}
		return nil
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

		if err := ensureDirectMode("forget requires direct database access"); err != nil {
			return HandleError("%v", err)
		}

		key := args[0]
		storageKey := kvPrefix + memoryPrefix + key

		ctx := rootCtx

		existing, _ := store.GetConfig(ctx, storageKey)
		if existing == "" {
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

		if err := store.DeleteConfig(ctx, storageKey); err != nil {
			return HandleErrorRespectJSON("forgetting memory: %v", err)
		}
		commandDidWrite.Store(true)

		if jsonOutput {
			return outputJSON(map[string]string{
				"key":     key,
				"deleted": "true",
			})
		}
		fmt.Printf("Forgot [%s]: %s\n", key, truncateMemory(existing, 80))
		return nil
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

		if err := ensureDirectMode("recall requires direct database access"); err != nil {
			return HandleError("%v", err)
		}

		key := args[0]
		storageKey := kvPrefix + memoryPrefix + key

		ctx := rootCtx
		value, err := store.GetConfig(ctx, storageKey)
		if err != nil {
			return HandleErrorRespectJSON("recalling memory: %v", err)
		}

		if jsonOutput {
			result := map[string]interface{}{
				"key":   key,
				"value": value,
				"found": value != "",
			}
			if replacement := isSuperseded(key); replacement != "" {
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
		if replacement := isSuperseded(key); replacement != "" {
			fmt.Printf("[SUPERSEDED by %s]\n%s\n", replacement, value)
		} else {
			fmt.Printf("%s\n", value)
		}
		return nil
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
	Run: func(cmd *cobra.Command, args []string) {
		CheckReadonly("memory supersede")

		if err := ensureDirectMode("memory supersede requires direct database access"); err != nil {
			FatalError("%v", err)
		}

		oldKey := args[0]
		newKey := memorySupersedeWithFlag

		if newKey == "" {
			FatalErrorRespectJSON("--with flag is required: bd memory supersede <old-key> --with=<new-key>")
		}

		ctx := rootCtx

		// Verify old memory exists
		oldStorageKey := kvPrefix + memoryPrefix + oldKey
		oldValue, err := store.GetConfig(ctx, oldStorageKey)
		if err != nil {
			FatalErrorRespectJSON("reading memory %q: %v", oldKey, err)
		}
		if oldValue == "" {
			FatalErrorRespectJSON("no memory with key %q", oldKey)
		}

		// Verify new memory exists
		newStorageKey := kvPrefix + memoryPrefix + newKey
		newValue, err := store.GetConfig(ctx, newStorageKey)
		if err != nil {
			FatalErrorRespectJSON("reading memory %q: %v", newKey, err)
		}
		if newValue == "" {
			FatalErrorRespectJSON("no memory with key %q (use --with to specify an existing memory)", newKey)
		}

		// Store the supersession edge
		superStorageKey := kvPrefix + memorySupersededPrefix + oldKey
		if err := store.SetConfig(ctx, superStorageKey, newKey); err != nil {
			FatalErrorRespectJSON("storing supersession: %v", err)
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
