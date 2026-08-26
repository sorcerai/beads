package main

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func TestMigratedRunECommandsSilenceCobraErrors(t *testing.T) {
	commands := map[string]*cobra.Command{
		"arch init":        archInitCmd,
		"arch check":       archCheckCmd,
		"arch draft":       archDraftCmd,
		"board":            boardCmd,
		"docs init":        docsInitCmd,
		"docs log":         docsLogCmd,
		"docs regen":       docsRegenCmd,
		"docs status":      docsStatusCmd,
		"explain":          explainCmd,
		"memory supersede": memorySupersedeCmd,
	}
	for name, command := range commands {
		if !command.SilenceErrors || !command.SilenceUsage {
			t.Errorf("%s must silence Cobra errors and usage after returning HandleError", name)
		}
	}
}

func TestDocsLogFailureHasNoCobraNoise(t *testing.T) {
	previousJSONOutput := jsonOutput
	jsonOutput = false
	t.Cleanup(func() { jsonOutput = previousJSONOutput })

	command := &cobra.Command{
		Use:           docsLogCmd.Use,
		Args:          docsLogCmd.Args,
		SilenceUsage:  docsLogCmd.SilenceUsage,
		SilenceErrors: docsLogCmd.SilenceErrors,
		RunE:          docsLogCmd.RunE,
	}
	command.Flags().String("since", "", "")

	var commandErr error
	stderr := captureStderr(t, func() {
		commandErr = command.Execute()
	})
	if commandErr == nil {
		t.Fatal("docs log without --since succeeded")
	}
	if strings.Count(stderr, "Error:") != 1 {
		t.Errorf("stderr should contain exactly one error:\n%s", stderr)
	}
	if strings.Contains(stderr, "Usage:") || strings.Contains(stderr, "exit code 1") {
		t.Errorf("stderr contains Cobra noise:\n%s", stderr)
	}
}

func TestMemorySupersedeDirectModeFailureIsJSON(t *testing.T) {
	previousJSONOutput := jsonOutput
	previousStore := store
	previousCommandContext := cmdCtx
	previousContextStore := previousStore
	if previousCommandContext != nil {
		previousContextStore = previousCommandContext.Store
	}
	previousStoreActive := isStoreActive()
	jsonOutput = true
	t.Setenv("BEADS_DIR", "")
	t.Setenv("BEADS_DB", "")
	temporaryDirectory := t.TempDir()
	previousWorkingDirectory, err := os.Getwd()
	if err != nil {
		t.Fatalf("get working directory: %v", err)
	}
	if err := os.Chdir(temporaryDirectory); err != nil {
		t.Fatalf("enter temporary directory: %v", err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(previousWorkingDirectory); err != nil {
			t.Errorf("restore working directory: %v", err)
		}
	})

	resetCommandContext()
	setStore(nil)
	setStoreActive(false)
	t.Cleanup(func() {
		cmdCtx = previousCommandContext
		if cmdCtx != nil {
			cmdCtx.Store = previousContextStore
		}
		store = previousStore
		setStoreActive(previousStoreActive)
		jsonOutput = previousJSONOutput
	})

	var commandErr error
	stdout := captureStdout(t, func() error {
		commandErr = memorySupersedeCmd.RunE(memorySupersedeCmd, []string{"old"})
		return nil
	})
	if commandErr == nil {
		t.Fatal("memory supersede without an active store succeeded")
	}
	if strings.Contains(stdout, "Error:") {
		t.Fatalf("JSON output contains text error:\n%s", stdout)
	}
	var response map[string]any
	if err := json.Unmarshal([]byte(stdout), &response); err != nil {
		t.Fatalf("invalid JSON error output: %v\n%s", err, stdout)
	}
	if _, ok := response["error"]; !ok {
		t.Fatalf("JSON output has no error field:\n%s", stdout)
	}
	errorMessage, ok := response["error"].(string)
	if !ok || !strings.Contains(errorMessage, "no beads database found") {
		t.Fatalf("JSON error = %#v, want no active-store error", response["error"])
	}
}
