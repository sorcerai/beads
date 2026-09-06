package main

import (
	"strings"
	"testing"
)

// TestManagedHookNames pins the set of git hooks beads installs. It is a
// change-detector on purpose: adding a name here without adding the matching
// case to hooksRunCmd installs a hook that fails with "unknown hook" on every
// git operation, which is exactly the drift the three separate name lists used
// to allow before install, status and uninstall were pointed at this one.
func TestManagedHookNames(t *testing.T) {
	want := []string{"pre-commit", "post-commit", "post-merge", "pre-push", "post-checkout", "prepare-commit-msg"}
	if len(managedHookNames) != len(want) {
		t.Fatalf("managedHookNames = %v, want %v", managedHookNames, want)
	}
	for i, name := range want {
		if managedHookNames[i] != name {
			t.Fatalf("managedHookNames = %v, want %v", managedHookNames, want)
		}
	}
}

// TestManagedHooksAreRunnableAndDocumented is the guard the name list alone
// cannot give: every managed hook must have a case in `bd hooks run` and a line
// in the help text a user reads to find out what got installed.
func TestManagedHooksAreRunnableAndDocumented(t *testing.T) {
	for _, name := range managedHookNames {
		if !strings.Contains(hooksRunCmd.Long, "- "+name+":") {
			t.Errorf("%s is installed but not listed in `bd hooks run` help", name)
		}
		if !strings.Contains(hooksInstallCmd.Long, "- "+name+":") {
			t.Errorf("%s is installed but not listed in `bd hooks install` help", name)
		}
		if !strings.Contains(hooksCmd.Long, "- "+name+":") {
			t.Errorf("%s is installed but not listed in `bd hooks` help", name)
		}
	}
}

func TestCodemapHookTimeout(t *testing.T) {
	for _, tc := range []struct {
		env  string
		want string
	}{
		{"", "10s"},
		{"3s", "3s"},
		{"25", "25s"},
		{"nonsense", "10s"},
		{"0", "10s"},
	} {
		t.Setenv(codemapHookTimeoutEnv, tc.env)
		if got := codemapHookTimeout().String(); got != tc.want {
			t.Errorf("%s=%q → %s, want %s", codemapHookTimeoutEnv, tc.env, got, tc.want)
		}
	}
}
