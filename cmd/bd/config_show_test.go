package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/config"
)

// TestViperSourceLabel verifies source label formatting for different config sources.
func TestViperSourceLabel(t *testing.T) {
	tests := []struct {
		name   string
		key    string
		source config.ConfigSource
		want   string
	}{
		{
			name:   "default source",
			key:    "backup.enabled",
			source: config.SourceDefault,
			want:   "default",
		},
		{
			name:   "config file source",
			key:    "git-remote",
			source: config.SourceConfigFile,
			want:   "config.yaml",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := viperSourceLabel(tt.key, tt.source)
			if got != tt.want {
				t.Errorf("viperSourceLabel(%q, %v) = %q, want %q", tt.key, tt.source, got, tt.want)
			}
		})
	}
}

// TestViperSourceLabelEnvVar verifies env var source includes the variable name.
func TestViperSourceLabelEnvVar(t *testing.T) {
	t.Setenv("BD_ACTOR", "test-bot")

	got := viperSourceLabel("actor", config.SourceEnvVar)
	if got != "env: BD_ACTOR" {
		t.Errorf("viperSourceLabel with env var = %q, want %q", got, "env: BD_ACTOR")
	}
}

// TestIsContainerKey verifies container key detection.
func TestIsContainerKey(t *testing.T) {
	tests := []struct {
		key  string
		want bool
	}{
		{"directory.labels", true},
		{"external_projects", true},
		{"repos", true},
		{"actor", false},
		{"backup.enabled", false},
		{"directory.labels.frontend", false},
	}

	for _, tt := range tests {
		t.Run(tt.key, func(t *testing.T) {
			if got := isContainerKey(tt.key); got != tt.want {
				t.Errorf("isContainerKey(%q) = %v, want %v", tt.key, got, tt.want)
			}
		})
	}
}

// TestFilterBySource verifies source filtering logic.
func TestFilterBySource(t *testing.T) {
	entries := []configEntry{
		{Key: "a", Value: "1", Source: "config.yaml"},
		{Key: "b", Value: "2", Source: "default"},
		{Key: "c", Value: "3", Source: "database"},
		{Key: "d", Value: "4", Source: "env: BD_ACTOR"},
		{Key: "e", Value: "5", Source: "config.yaml"},
		{Key: "f", Value: "6", Source: "metadata"},
		{Key: "g", Value: "7", Source: "git"},
	}

	tests := []struct {
		source string
		want   int
	}{
		{"config.yaml", 2},
		{"default", 1},
		{"database", 1},
		{"env", 1},
		{"metadata", 1},
		{"git", 1},
		{"nonexistent", 0},
	}

	for _, tt := range tests {
		t.Run(tt.source, func(t *testing.T) {
			filtered := filterBySource(entries, tt.source)
			if len(filtered) != tt.want {
				t.Errorf("filterBySource(%q) returned %d entries, want %d", tt.source, len(filtered), tt.want)
			}
		})
	}
}

// TestCollectMetadataEntries verifies metadata.json field collection.
func TestCollectMetadataEntries(t *testing.T) {
	// Create a temp directory with a metadata.json
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("Failed to create .beads dir: %v", err)
	}

	metadataJSON := `{
  "database": "beads.db",
  "dolt_mode": "embedded",
  "project_id": "abc-123",
  "dolt_server_port": 3307,
  "dolt_server_tls": true
}`
	metadataPath := filepath.Join(beadsDir, "metadata.json")
	if err := os.WriteFile(metadataPath, []byte(metadataJSON), 0600); err != nil {
		t.Fatalf("Failed to write metadata.json: %v", err)
	}

	// Change to temp dir so FindBeadsDir can find it
	origDir, _ := os.Getwd()
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("Failed to chdir: %v", err)
	}
	defer os.Chdir(origDir) //nolint:errcheck

	entries := collectMetadataEntries()

	// Verify expected entries exist
	entryMap := make(map[string]configEntry)
	for _, e := range entries {
		entryMap[e.Key] = e
	}

	if e, ok := entryMap["dolt_mode"]; !ok {
		t.Error("expected dolt_mode in metadata entries")
	} else if e.Value != "embedded" {
		t.Errorf("dolt_mode = %q, want %q", e.Value, "embedded")
	} else if e.Source != "metadata" {
		t.Errorf("dolt_mode source = %q, want %q", e.Source, "metadata")
	}

	if e, ok := entryMap["project_id"]; !ok {
		t.Error("expected project_id in metadata entries")
	} else if e.Value != "abc-123" {
		t.Errorf("project_id = %q, want %q", e.Value, "abc-123")
	}

	if e, ok := entryMap["dolt_server_port"]; !ok {
		t.Error("expected dolt_server_port in metadata entries")
	} else if e.Value != "3307" {
		t.Errorf("dolt_server_port = %q, want %q", e.Value, "3307")
	}

	if e, ok := entryMap["dolt_server_tls"]; !ok {
		t.Error("expected dolt_server_tls in metadata entries")
	} else if e.Value != "true" {
		t.Errorf("dolt_server_tls = %q, want %q", e.Value, "true")
	}
}

// TestCollectMetadataEntriesNoBeadsDir verifies graceful handling when no .beads exists.
func TestCollectMetadataEntriesNoBeadsDir(t *testing.T) {
	tmpDir := t.TempDir()
	origDir, _ := os.Getwd()
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("Failed to chdir: %v", err)
	}
	defer os.Chdir(origDir) //nolint:errcheck

	entries := collectMetadataEntries()
	if len(entries) != 0 {
		t.Errorf("expected no metadata entries without .beads dir, got %d", len(entries))
	}
}

// TestCollectViperEntries verifies that Viper key collection works with initialized config.
func TestCollectViperEntries(t *testing.T) {
	// Create a temp dir with config.yaml
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("Failed to create .beads dir: %v", err)
	}
	configPath := filepath.Join(beadsDir, "config.yaml")
	if err := os.WriteFile(configPath, []byte("git-remote: \"https://example.com/repo\"\n"), 0600); err != nil {
		t.Fatalf("Failed to write config.yaml: %v", err)
	}

	origDir, _ := os.Getwd()
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("Failed to chdir: %v", err)
	}
	defer os.Chdir(origDir) //nolint:errcheck

	// Re-initialize config to pick up our test config.yaml
	t.Setenv("BEADS_TEST_IGNORE_REPO_CONFIG", "1")
	config.ResetForTesting()
	if err := config.Initialize(); err != nil {
		t.Fatalf("config.Initialize() failed: %v", err)
	}
	defer func() {
		config.ResetForTesting()
		// Re-initialize with original config
		os.Chdir(origDir) //nolint:errcheck
		_ = config.Initialize()
	}()

	entries := collectViperEntries()

	// Check that we got some entries
	if len(entries) == 0 {
		t.Fatal("expected at least some Viper entries")
	}

	// Check git-remote is present with config.yaml source
	found := false
	for _, e := range entries {
		if e.Key == "git-remote" {
			found = true
			if e.Value != "https://example.com/repo" {
				t.Errorf("git-remote value = %q, want %q", e.Value, "https://example.com/repo")
			}
			if e.Source != "config.yaml" {
				t.Errorf("git-remote source = %q, want %q", e.Source, "config.yaml")
			}
			break
		}
	}
	if !found {
		t.Error("expected git-remote key in Viper entries")
	}

	// Verify defaults with non-empty values are included
	foundDefault := false
	for _, e := range entries {
		if e.Source == "default" && e.Value != "" {
			foundDefault = true
			break
		}
	}
	if !foundDefault {
		t.Error("expected at least one default entry with a non-empty value")
	}

	// Verify empty defaults are excluded
	for _, e := range entries {
		if e.Source == "default" && e.Value == "" {
			t.Errorf("empty default %q should be excluded", e.Key)
		}
	}
}

// TestCollectViperEntriesWithEnvOverride verifies env var source detection.
func TestCollectViperEntriesWithEnvOverride(t *testing.T) {
	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("Failed to create .beads dir: %v", err)
	}
	configPath := filepath.Join(beadsDir, "config.yaml")
	if err := os.WriteFile(configPath, []byte("actor: from-config\n"), 0600); err != nil {
		t.Fatalf("Failed to write config.yaml: %v", err)
	}

	origDir, _ := os.Getwd()
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("Failed to chdir: %v", err)
	}
	defer os.Chdir(origDir) //nolint:errcheck

	t.Setenv("BD_ACTOR", "env-bot")
	t.Setenv("BEADS_TEST_IGNORE_REPO_CONFIG", "1")
	config.ResetForTesting()
	if err := config.Initialize(); err != nil {
		t.Fatalf("config.Initialize() failed: %v", err)
	}
	defer func() {
		config.ResetForTesting()
		os.Chdir(origDir) //nolint:errcheck
		_ = config.Initialize()
	}()

	entries := collectViperEntries()

	for _, e := range entries {
		if e.Key == "actor" {
			if e.Source != "env: BD_ACTOR" {
				t.Errorf("actor source = %q, want %q", e.Source, "env: BD_ACTOR")
			}
			return
		}
	}
	t.Error("expected actor key in Viper entries")
}

// TestCollectViperEntriesActorPrecedenceOverBDActor is a regression test for
// GH#4645: `bd config show` must report the same actor value AND provenance
// that mutations actually use (resolveConfiguredActor in main.go), not the
// deprecated BD_ACTOR that viper's AutomaticEnv binds ahead of any explicit
// binding. With both env vars set, the entry must show BEADS_ACTOR's value
// with "env: BEADS_ACTOR" provenance, never BD_ACTOR's.
func TestCollectViperEntriesActorPrecedenceOverBDActor(t *testing.T) {
	t.Setenv("BD_ACTOR", "from-bd-actor")
	t.Setenv("BEADS_ACTOR", "from-beads-actor")
	t.Setenv("BEADS_TEST_IGNORE_REPO_CONFIG", "1")
	config.ResetForTesting()
	if err := config.Initialize(); err != nil {
		t.Fatalf("config.Initialize() failed: %v", err)
	}
	defer func() {
		config.ResetForTesting()
		_ = config.Initialize()
	}()

	entries := collectViperEntries()

	for _, e := range entries {
		if e.Key == "actor" {
			if e.Value != "from-beads-actor" {
				t.Errorf("actor value = %q, want %q (BEADS_ACTOR must win)", e.Value, "from-beads-actor")
			}
			if e.Source != "env: BEADS_ACTOR" {
				t.Errorf("actor source = %q, want %q", e.Source, "env: BEADS_ACTOR")
			}
			return
		}
	}
	t.Error("expected actor key in Viper entries")
}

// TestCollectViperEntriesUserGlobalProvenance verifies that user-global
// keys report the user-global value AND the user-global config path as
// their source, even when a project .beads/config.yaml sets a conflicting value
// that the runtime ignores. This is the provenance contract `bd config show`
// shares with `bd config get`: user-global keys live in the user-global
// config only, so the displayed value must never be attributed to a project file.
func TestCollectViperEntriesUserGlobalProvenance(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))

	// User-global config sets node_id; this is the value the runtime honors.
	userCfgDir := filepath.Join(home, ".config", "bd")
	if err := os.MkdirAll(userCfgDir, 0o755); err != nil {
		t.Fatalf("mkdir user config dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(userCfgDir, "config.yaml"), []byte("node_id: user-machine\n"), 0o600); err != nil {
		t.Fatalf("write user config: %v", err)
	}

	// A project config tries to override node_id through the highest-precedence
	// BEADS_DIR config. The runtime ignores it, but it makes GetValueSource report
	// SourceConfigFile so the regression covers the misattribution case.
	projectBeadsDir := filepath.Join(t.TempDir(), ".beads")
	if err := os.MkdirAll(projectBeadsDir, 0o755); err != nil {
		t.Fatalf("mkdir project .beads: %v", err)
	}
	if err := os.WriteFile(filepath.Join(projectBeadsDir, "config.yaml"), []byte("node_id: project-machine\n"), 0o644); err != nil {
		t.Fatalf("write project config: %v", err)
	}
	t.Setenv("BEADS_DIR", projectBeadsDir)

	config.ResetForTesting()
	if err := config.Initialize(); err != nil {
		t.Fatalf("config.Initialize() failed: %v", err)
	}
	defer func() {
		config.ResetForTesting()
		_ = config.Initialize()
	}()

	var entry *configEntry
	entries := collectViperEntries()
	for i := range entries {
		if entries[i].Key == "node_id" {
			entry = &entries[i]
			break
		}
	}
	if entry == nil {
		t.Fatal("expected node_id in Viper entries")
	}

	// Value comes from the user-global file, not the project override.
	if entry.Value != "user-machine" {
		t.Errorf("node_id value = %q, want %q (user-global, not project override)", entry.Value, "user-machine")
	}
	// Source is the explicit user-global path, matching `bd config get`, not the
	// generic project "config.yaml" label.
	wantSource, err := config.UserConfigYamlPath()
	if err != nil {
		t.Fatalf("resolve user config path: %v", err)
	}
	if entry.Source != wantSource {
		t.Errorf("node_id source = %q, want %q (user-global path)", entry.Source, wantSource)
	}
	if entry.Source == "config.yaml" {
		t.Error("node_id source must not be the generic project config.yaml label")
	}

	// The `config show --json` output serializes exactly these fields, so assert
	// the marshaled entry reports the user-global value and never the project
	// "config.yaml" provenance the runtime ignores.
	encoded, err := json.Marshal(entry)
	if err != nil {
		t.Fatalf("json.Marshal(entry): %v", err)
	}
	if !strings.Contains(string(encoded), `"value":"user-machine"`) {
		t.Errorf("config show --json entry %s missing user-global value", encoded)
	}
	if strings.Contains(string(encoded), `"source":"config.yaml"`) {
		t.Errorf("config show --json entry %s misattributes user-global value to project config.yaml", encoded)
	}
}

func TestConfigShowAndListRedactSecrets(t *testing.T) {
	fake := setupConfigOutputTest(t)

	const (
		yamlSecret = "yaml-aggregate-secret"
		dbSecret   = "database-aggregate-secret"
		yamlValue  = "visible-yaml-value"
		dbValue    = "visible-database-value"
	)
	if err := config.SetYamlConfig("ado.pat", yamlSecret); err != nil {
		t.Fatalf("set YAML secret: %v", err)
	}
	if err := config.SetYamlConfig("actor", yamlValue); err != nil {
		t.Fatalf("set YAML non-secret: %v", err)
	}
	fake.values["custom.password"] = dbSecret
	fake.values["custom.visible"] = dbValue

	config.ResetForTesting()
	if err := config.Initialize(); err != nil {
		t.Fatalf("reload config: %v", err)
	}

	tests := []struct {
		name        string
		run         func() error
		jsonOutput  bool
		markerCount int
		wantValues  map[string]string
		jsonList    bool
		jsonShow    bool
	}{
		{
			name:        "show text",
			run:         func() error { return configShowCmd.RunE(configShowCmd, nil) },
			markerCount: 2,
			wantValues:  map[string]string{"actor": yamlValue, "custom.visible": dbValue},
		},
		{
			name:        "show JSON",
			run:         func() error { return configShowCmd.RunE(configShowCmd, nil) },
			jsonOutput:  true,
			markerCount: 2,
			wantValues: map[string]string{
				"ado.pat":         "[REDACTED]",
				"custom.password": "[REDACTED]",
				"actor":           yamlValue,
				"custom.visible":  dbValue,
			},
			jsonShow: true,
		},
		{
			name:        "list text",
			run:         func() error { return configListCmd.RunE(configListCmd, nil) },
			markerCount: 2,
			wantValues:  map[string]string{"actor": yamlValue, "custom.visible": dbValue},
		},
		{
			name:        "list JSON",
			run:         func() error { return configListCmd.RunE(configListCmd, nil) },
			jsonOutput:  true,
			markerCount: 1,
			wantValues: map[string]string{
				"custom.password": "[REDACTED]",
				"custom.visible":  dbValue,
			},
			jsonList: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			jsonOutput = tt.jsonOutput
			out := captureStdout(t, tt.run)

			for _, secret := range []string{yamlSecret, dbSecret} {
				if strings.Contains(out, secret) {
					t.Errorf("config %s leaked secret %q:\n%s", tt.name, secret, out)
				}
			}
			if got := strings.Count(out, "[REDACTED]"); got != tt.markerCount {
				t.Errorf("config %s contains %d redaction markers, want %d:\n%s", tt.name, got, tt.markerCount, out)
			}

			if tt.jsonShow {
				var entries []configEntry
				if err := json.Unmarshal([]byte(out), &entries); err != nil {
					t.Fatalf("parse config show JSON: %v\n%s", err, out)
				}
				got := make(map[string]string, len(entries))
				for _, entry := range entries {
					got[entry.Key] = entry.Value
				}
				for key, want := range tt.wantValues {
					if got[key] != want {
						t.Errorf("config show JSON value for %q = %q, want %q", key, got[key], want)
					}
				}
				return
			}

			if tt.jsonList {
				var payload map[string]interface{}
				if err := json.Unmarshal([]byte(out), &payload); err != nil {
					t.Fatalf("parse config list JSON: %v\n%s", err, out)
				}
				for key, want := range tt.wantValues {
					got, _ := payload[key].(string)
					if got != want {
						t.Errorf("config list JSON value for %q = %q, want %q", key, got, want)
					}
				}
				return
			}

			for key, value := range tt.wantValues {
				if !strings.Contains(out, key) || !strings.Contains(out, value) {
					t.Errorf("config %s changed non-secret output for %q=%q:\n%s", tt.name, key, value, out)
				}
			}
		})
	}
}
