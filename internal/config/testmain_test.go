package config

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// TestMain isolates tests from the repository's own `.beads/config.yaml` and
// from an ambient workspace pointer.
//
// Tests expect config defaults. If the test process
// runs from within this repo, Initialize() will walk up from CWD and load
// the repo's tracked `.beads/config.yaml`, which may override defaults.
func TestMain(m *testing.M) {
	tmp, err := os.MkdirTemp("", "beads-config-tests-*")
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to create temp dir: %v\n", err)
		os.Exit(1)
	}

	oldWD, _ := os.Getwd()

	// Point config discovery away from the repo and user's machine.
	_ = os.Chdir(tmp)
	_ = os.Setenv("HOME", tmp)
	_ = os.Setenv("USERPROFILE", tmp) // Windows compatibility
	_ = os.Setenv("XDG_CONFIG_HOME", filepath.Join(tmp, "xdg-config"))

	// Clear an inherited BEADS_DIR so tests that don't call envSnapshot stay
	// hermetic. When BEADS_DIR is set (e.g. inside a gastown agent session),
	// Initialize() loads that workspace's config at highest priority and
	// short-circuits the CWD walk (config.go), so tests that write a temp
	// .beads/config.yaml and chdir into it (TestValidationConfigFromFile,
	// TestFederationConfigFromFile, ...) silently read the ambient config
	// instead of their own. GitHub CI has no BEADS_DIR, which is why this only
	// bites local runs. Mirrors cmd/bd's TestMain. (be-jsr)
	origBeadsDir, hadBeadsDir := os.LookupEnv("BEADS_DIR")
	_ = os.Unsetenv("BEADS_DIR")

	code := m.Run()

	_ = os.Chdir(oldWD)
	if hadBeadsDir {
		_ = os.Setenv("BEADS_DIR", origBeadsDir)
	}
	_ = os.RemoveAll(tmp)
	os.Exit(code)
}
