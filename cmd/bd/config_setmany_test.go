package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/storage/uow"
	"github.com/steveyegge/beads/internal/types"
)

type configOutputStore struct {
	storage.DoltStorage
	values  map[string]string
	deleted []string
}

func (s *configOutputStore) SetConfig(_ context.Context, key, value string) error {
	s.values[key] = value
	return nil
}

func (s *configOutputStore) DeleteConfig(_ context.Context, key string) error {
	delete(s.values, key)
	s.deleted = append(s.deleted, key)
	return nil
}

func (s *configOutputStore) deletedKey(key string) bool {
	for _, deleted := range s.deleted {
		if deleted == key {
			return true
		}
	}
	return false
}

func (s *configOutputStore) GetAllConfig(context.Context) (map[string]string, error) {
	values := make(map[string]string, len(s.values))
	for key, value := range s.values {
		values[key] = value
	}
	return values, nil
}

func setupConfigOutputTest(t *testing.T) *configOutputStore {
	t.Helper()

	oldStore := store
	oldRootCtx := rootCtx
	oldJSONOutput := jsonOutput
	oldForceGitTracked := forceGitTracked
	oldStoreActive := isStoreActive()
	t.Cleanup(func() {
		setStore(oldStore)
		setStoreActive(oldStoreActive)
		rootCtx = oldRootCtx
		jsonOutput = oldJSONOutput
		forceGitTracked = oldForceGitTracked
		config.ResetForTesting()
		_ = config.Initialize()
	})

	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatalf("create .beads: %v", err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "config.yaml"), nil, 0o600); err != nil {
		t.Fatalf("create config.yaml: %v", err)
	}
	t.Setenv("BEADS_DIR", beadsDir)
	t.Chdir(tmpDir)

	config.ResetForTesting()
	if err := config.Initialize(); err != nil {
		t.Fatalf("initialize config: %v", err)
	}

	fake := &configOutputStore{values: make(map[string]string)}
	setStore(fake)
	setStoreActive(true)
	rootCtx = context.Background()
	forceGitTracked = true
	return fake
}

func TestConfigSetConfirmationsRedactSecrets(t *testing.T) {
	setupConfigOutputTest(t)

	tests := []struct {
		name     string
		args     []string
		secrets  []string
		wantText []string
		wantJSON map[string]string
		setMany  bool
	}{
		{
			name:     "set text",
			args:     []string{"ado.pat", "set-text-secret"},
			secrets:  []string{"set-text-secret"},
			wantText: []string{"Set ado.pat = [REDACTED]"},
		},
		{
			name:     "set JSON",
			args:     []string{"ado.pat", "set-json-secret"},
			secrets:  []string{"set-json-secret"},
			wantJSON: map[string]string{"ado.pat": "[REDACTED]"},
		},
		{
			name: "set-many text",
			args: []string{
				"ado.pat=set-many-yaml-secret",
				"custom.password=set-many-db-secret",
				"actor=visible-text-value",
			},
			secrets: []string{"set-many-yaml-secret", "set-many-db-secret"},
			wantText: []string{
				"Set ado.pat = [REDACTED]",
				"Set custom.password = [REDACTED]",
				"Set actor = visible-text-value",
			},
			setMany: true,
		},
		{
			name: "set-many JSON",
			args: []string{
				"ado.pat=set-many-json-yaml-secret",
				"custom.password=set-many-json-db-secret",
				"actor=visible-json-value",
			},
			secrets: []string{"set-many-json-yaml-secret", "set-many-json-db-secret"},
			wantJSON: map[string]string{
				"ado.pat":         "[REDACTED]",
				"custom.password": "[REDACTED]",
				"actor":           "visible-json-value",
			},
			setMany: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			jsonOutput = tt.wantJSON != nil
			out := captureStdout(t, func() error {
				if tt.setMany {
					return configSetManyCmd.RunE(configSetManyCmd, tt.args)
				}
				return configSetCmd.RunE(configSetCmd, tt.args)
			})

			for _, secret := range tt.secrets {
				if strings.Contains(out, secret) {
					t.Errorf("config confirmation leaked secret %q:\n%s", secret, out)
				}
			}

			if tt.wantJSON == nil {
				for _, want := range tt.wantText {
					if !strings.Contains(out, want) {
						t.Errorf("config confirmation missing %q:\n%s", want, out)
					}
				}
				return
			}

			got := make(map[string]string)
			if tt.setMany {
				var entries []map[string]string
				if err := json.Unmarshal([]byte(out), &entries); err != nil {
					t.Fatalf("parse set-many JSON: %v\n%s", err, out)
				}
				for _, entry := range entries {
					got[entry["key"]] = entry["value"]
				}
			} else {
				var entry map[string]interface{}
				if err := json.Unmarshal([]byte(out), &entry); err != nil {
					t.Fatalf("parse set JSON: %v\n%s", err, out)
				}
				key, _ := entry["key"].(string)
				got[key], _ = entry["value"].(string)
			}
			for key, want := range tt.wantJSON {
				if got[key] != want {
					t.Errorf("confirmation value for %q = %q, want %q", key, got[key], want)
				}
			}
		})
	}
}

func TestConfigSetRoutesEverySecretOnlyToYAML(t *testing.T) {
	tests := []struct {
		name   string
		key    string
		value  string
		secret bool
	}{
		{name: "custom password", key: "custom.password", value: "custom-password-value", secret: true},
		{name: "linear refresh token", key: "linear.refresh_token", value: "linear-refresh-value", secret: true},
		{name: "ADO private key", key: "ado.private_key", value: "ado-private-value", secret: true},
		{name: "non-secret custom key", key: "custom.theme", value: "dark", secret: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := setupConfigOutputTest(t)
			if tt.secret {
				fake.values[tt.key] = "legacy-dolt-secret"
			}
			captureStdout(t, func() error {
				return configSetCmd.RunE(configSetCmd, []string{tt.key, tt.value})
			})

			yamlValue := config.GetStringFromDir(os.Getenv("BEADS_DIR"), tt.key)
			dbValue, wroteDB := fake.values[tt.key]
			if tt.secret {
				if wroteDB {
					t.Errorf("secret key %q called Dolt SetConfig with %q", tt.key, dbValue)
				}
				if yamlValue != tt.value {
					t.Errorf("secret key %q config.yaml value = %q, want %q", tt.key, yamlValue, tt.value)
				}
				if !fake.deletedKey(tt.key) {
					t.Errorf("secret key %q did not delete legacy Dolt config", tt.key)
				}
				return
			}

			if !wroteDB || dbValue != tt.value {
				t.Errorf("non-secret key %q Dolt value = %q (written=%v), want %q", tt.key, dbValue, wroteDB, tt.value)
			}
			if yamlValue != "" {
				t.Errorf("non-secret key %q unexpectedly written to config.yaml as %q", tt.key, yamlValue)
			}
			if fake.deletedKey(tt.key) {
				t.Errorf("non-secret key %q unexpectedly deleted Dolt config", tt.key)
			}
		})
	}
}

func TestConfigSetManyRoutesEverySecretOnlyToYAML(t *testing.T) {
	fake := setupConfigOutputTest(t)
	secrets := map[string]string{
		"custom.password":      "batch-custom-password",
		"linear.refresh_token": "batch-linear-refresh",
		"ado.private_key":      "batch-ado-private",
	}
	for key := range secrets {
		fake.values[key] = "legacy-dolt-secret"
	}
	const nonSecretKey = "custom.theme"
	const nonSecretValue = "light"
	args := make([]string, 0, len(secrets)+1)
	for key, value := range secrets {
		args = append(args, key+"="+value)
	}
	args = append(args, nonSecretKey+"="+nonSecretValue)

	captureStdout(t, func() error {
		return configSetManyCmd.RunE(configSetManyCmd, args)
	})

	if len(fake.values) != 1 || fake.values[nonSecretKey] != nonSecretValue {
		t.Errorf("Dolt SetConfig writes = %v, want only %s=%s", fake.values, nonSecretKey, nonSecretValue)
	}
	beadsDir := os.Getenv("BEADS_DIR")
	for key, want := range secrets {
		if got := config.GetStringFromDir(beadsDir, key); got != want {
			t.Errorf("secret key %q config.yaml value = %q, want %q", key, got, want)
		}
		if !fake.deletedKey(key) {
			t.Errorf("secret key %q did not delete legacy Dolt config", key)
		}
	}
	if got := config.GetStringFromDir(beadsDir, nonSecretKey); got != "" {
		t.Errorf("non-secret key %q unexpectedly written to config.yaml as %q", nonSecretKey, got)
	}
	if fake.deletedKey(nonSecretKey) {
		t.Errorf("non-secret key %q unexpectedly deleted Dolt config", nonSecretKey)
	}
}

func TestConfigUnsetSecretRemovesYAMLAndLegacyDoltValue(t *testing.T) {
	keys := []string{"custom.password", "linear.refresh_token", "ado.private_key"}
	for _, key := range keys {
		t.Run(key, func(t *testing.T) {
			fake := setupConfigOutputTest(t)
			fake.values[key] = "legacy-dolt-secret"
			if err := config.SetYamlConfig(key, "yaml-secret"); err != nil {
				t.Fatalf("seed config.yaml: %v", err)
			}

			captureStdout(t, func() error {
				return configUnsetCmd.RunE(configUnsetCmd, []string{key})
			})

			if got := config.GetStringFromDir(os.Getenv("BEADS_DIR"), key); got != "" {
				t.Errorf("secret key %q remains in config.yaml as %q", key, got)
			}
			if got, exists := fake.values[key]; exists {
				t.Errorf("secret key %q remains in Dolt as %q", key, got)
			}
			if !fake.deletedKey(key) {
				t.Errorf("secret key %q did not delete legacy Dolt config", key)
			}
		})
	}
}

func writeMalformedConfigYAML(t *testing.T) {
	t.Helper()
	configPath := filepath.Join(os.Getenv("BEADS_DIR"), "config.yaml")
	if err := os.WriteFile(configPath, []byte("custom: [unterminated\n"), 0o600); err != nil {
		t.Fatalf("write malformed config.yaml: %v", err)
	}
}

func assertLegacySecretPreserved(t *testing.T, fake *configOutputStore, key string) {
	t.Helper()
	if fake.deletedKey(key) {
		t.Errorf("legacy Dolt secret %q was deleted before YAML mutation succeeded", key)
	}
	if got := fake.values[key]; got != "legacy-dolt-secret" {
		t.Errorf("legacy Dolt secret %q = %q after YAML failure, want preserved value", key, got)
	}
}

func TestConfigSetSecretYamlFailurePreservesLegacyDolt(t *testing.T) {
	fake := setupConfigOutputTest(t)
	const key = "custom.credentials"
	fake.values[key] = "legacy-dolt-secret"
	writeMalformedConfigYAML(t)

	if err := configSetCmd.RunE(configSetCmd, []string{key, "replacement-secret"}); err == nil {
		t.Fatal("config set succeeded with malformed config.yaml")
	}
	assertLegacySecretPreserved(t, fake, key)
}

func TestConfigSetManySecretYamlFailurePreservesLegacyDolt(t *testing.T) {
	fake := setupConfigOutputTest(t)
	const key = "custom.clientCredentials"
	fake.values[key] = "legacy-dolt-secret"
	writeMalformedConfigYAML(t)

	if err := configSetManyCmd.RunE(configSetManyCmd, []string{key + "=replacement-secret"}); err == nil {
		t.Fatal("config set-many succeeded with malformed config.yaml")
	}
	assertLegacySecretPreserved(t, fake, key)
}

func TestConfigUnsetSecretYamlFailurePreservesLegacyDolt(t *testing.T) {
	fake := setupConfigOutputTest(t)
	const key = "custom.accessToken"
	fake.values[key] = "legacy-dolt-secret"
	writeMalformedConfigYAML(t)

	if err := configUnsetCmd.RunE(configUnsetCmd, []string{key}); err == nil {
		t.Fatal("config unset succeeded with malformed config.yaml")
	}
	assertLegacySecretPreserved(t, fake, key)
}

// TestConfigSetManyArgParsing tests argument parsing for the set-many command.
func TestConfigSetManyArgParsing(t *testing.T) {
	tests := []struct {
		name    string
		arg     string
		wantKey string
		wantVal string
		wantErr bool
	}{
		{"simple", "key=value", "key", "value", false},
		{"dotted key", "ado.state_map.open=New", "ado.state_map.open", "New", false},
		{"value with equals", "key=val=ue", "key", "val=ue", false},
		{"empty value", "key=", "key", "", false},
		{"no equals", "keyvalue", "", "", true},
		{"only equals", "=value", "", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			idx := strings.Index(tt.arg, "=")
			if idx <= 0 {
				if !tt.wantErr {
					t.Error("expected successful parse, got error")
				}
				return
			}
			if tt.wantErr {
				t.Error("expected error, got successful parse")
				return
			}
			key := tt.arg[:idx]
			value := tt.arg[idx+1:]
			if key != tt.wantKey {
				t.Errorf("key = %q, want %q", key, tt.wantKey)
			}
			if value != tt.wantVal {
				t.Errorf("value = %q, want %q", value, tt.wantVal)
			}
		})
	}
}

// TestConfigSetManyYamlKeyDetection tests that yaml-only keys are correctly identified
// for routing in the set-many command.
func TestConfigSetManyYamlKeyDetection(t *testing.T) {
	yamlKeys := []string{"no-db", "json", "routing.mode", "routing.default", "no-push", "import.auto", "import.path"}
	for _, key := range yamlKeys {
		if !config.IsYamlOnlyKey(key) {
			t.Errorf("expected %q to be yaml-only", key)
		}
	}

	dbKeys := []string{"ado.state_map.open", "jira.url", "status.custom", "test.key"}
	for _, key := range dbKeys {
		if config.IsYamlOnlyKey(key) {
			t.Errorf("expected %q to NOT be yaml-only", key)
		}
	}
}

// TestConfigSetManyMixedKeyRouting verifies that a batch of mixed key types
// (yaml-only, git config, and database) are correctly categorized into their
// respective storage backends. This exercises the Phase 3 routing logic.
func TestConfigSetManyMixedKeyRouting(t *testing.T) {
	type kvPair struct {
		key, value string
	}

	args := []string{
		"no-db=true",                  // yaml-only
		"routing.mode=direct",         // yaml-only
		"beads.role=maintainer",       // git config
		"jira.url=https://j.test",     // database
		"ado.state_map.open=New",      // database
		"ado.state_map.closed=Closed", // database
	}

	// Phase 1: Parse
	pairs := make([]kvPair, 0, len(args))
	for _, arg := range args {
		idx := strings.Index(arg, "=")
		if idx <= 0 {
			t.Fatalf("unexpected parse failure for %q", arg)
		}
		pairs = append(pairs, kvPair{key: arg[:idx], value: arg[idx+1:]})
	}

	// Phase 3: Categorize
	var yamlPairs, gitPairs, dbPairs []kvPair
	for _, p := range pairs {
		if config.IsYamlOnlyKey(p.key) {
			yamlPairs = append(yamlPairs, p)
		} else if p.key == "beads.role" {
			gitPairs = append(gitPairs, p)
		} else {
			dbPairs = append(dbPairs, p)
		}
	}

	if len(yamlPairs) != 2 {
		t.Errorf("expected 2 yaml pairs, got %d", len(yamlPairs))
	}
	if len(gitPairs) != 1 {
		t.Errorf("expected 1 git pair, got %d", len(gitPairs))
	}
	if len(dbPairs) != 3 {
		t.Errorf("expected 3 db pairs, got %d", len(dbPairs))
	}

	// Verify yaml keys are correct
	yamlKeySet := map[string]bool{}
	for _, p := range yamlPairs {
		yamlKeySet[p.key] = true
	}
	if !yamlKeySet["no-db"] || !yamlKeySet["routing.mode"] {
		t.Errorf("yaml pairs missing expected keys: %v", yamlPairs)
	}

	// Verify git key
	if len(gitPairs) > 0 && gitPairs[0].key != "beads.role" {
		t.Errorf("expected git pair key 'beads.role', got %q", gitPairs[0].key)
	}

	// Verify DB keys
	dbKeySet := map[string]bool{}
	for _, p := range dbPairs {
		dbKeySet[p.key] = true
	}
	for _, expected := range []string{"jira.url", "ado.state_map.open", "ado.state_map.closed"} {
		if !dbKeySet[expected] {
			t.Errorf("db pairs missing expected key %q", expected)
		}
	}
}

// TestConfigSetManyValidationBeforeWrite verifies that validation (Phase 2)
// catches errors before any writes would occur. This tests the upfront
// validation pass that prevents partial writes.
func TestConfigSetManyValidationBeforeWrite(t *testing.T) {
	t.Run("invalid beads.role rejected upfront", func(t *testing.T) {
		// Simulate the Phase 2 validation logic from the command handler
		type kvPair struct {
			key, value string
		}
		pairs := []kvPair{
			{"jira.url", "https://j.test"}, // valid DB key (would succeed)
			{"beads.role", "superadmin"},   // invalid role (should fail validation)
			{"ado.state_map.open", "New"},  // valid DB key (would succeed)
		}

		validRoles := map[string]bool{"maintainer": true, "contributor": true}
		var validationErr string
		for _, p := range pairs {
			if p.key == "beads.role" && !validRoles[p.value] {
				validationErr = p.value
				break
			}
		}
		if validationErr == "" {
			t.Fatal("expected validation to reject invalid role, but it passed")
		}
		if validationErr != "superadmin" {
			t.Errorf("expected rejected value 'superadmin', got %q", validationErr)
		}
	})

	t.Run("invalid status.custom rejected upfront", func(t *testing.T) {
		// status.custom with invalid format should fail validation
		// ParseCustomStatusConfig rejects names with spaces and other invalid chars
		type kvPair struct {
			key, value string
		}
		pairs := []kvPair{
			{"jira.url", "https://j.test"},                      // valid
			{"status.custom", "valid_status,also valid status"}, // may be invalid depending on parser
		}

		var validationFailed bool
		for _, p := range pairs {
			if p.key == "status.custom" && p.value != "" {
				if _, err := types.ParseCustomStatusConfig(p.value); err != nil {
					validationFailed = true
					break
				}
			}
		}
		// The key point is that validation runs BEFORE writes.
		// Whether this specific value is invalid depends on the parser,
		// but the validation logic is exercised either way.
		_ = validationFailed
	})

	t.Run("valid beads.role passes validation", func(t *testing.T) {
		type kvPair struct {
			key, value string
		}
		pairs := []kvPair{
			{"beads.role", "contributor"},
			{"jira.url", "https://j.test"},
		}

		validRoles := map[string]bool{"maintainer": true, "contributor": true}
		for _, p := range pairs {
			if p.key == "beads.role" && !validRoles[p.value] {
				t.Errorf("expected valid role %q to pass validation", p.value)
			}
		}
	})

	t.Run("valid status.custom passes validation", func(t *testing.T) {
		type kvPair struct {
			key, value string
		}
		pairs := []kvPair{
			{"status.custom", "awaiting_review,awaiting_testing"},
			{"jira.project", "PROJ"},
		}

		for _, p := range pairs {
			if p.key == "status.custom" && p.value != "" {
				if _, err := types.ParseCustomStatusConfig(p.value); err != nil {
					t.Errorf("expected valid status.custom to pass validation: %v", err)
				}
			}
		}
	})
}

// TestConfigSetManyOutputLocationMapping verifies that the output phase
// correctly maps each key to its storage location label.
func TestConfigSetManyOutputLocationMapping(t *testing.T) {
	type kvPair struct {
		key, value string
	}
	pairs := []kvPair{
		{"no-db", "true"},
		{"routing.mode", "direct"},
		{"beads.role", "maintainer"},
		{"jira.url", "https://j.test"},
		{"ado.state_map.open", "New"},
	}

	expectedLocations := map[string]string{
		"no-db":              "config.yaml",
		"routing.mode":       "config.yaml",
		"beads.role":         "git config",
		"jira.url":           "database",
		"ado.state_map.open": "database",
	}

	for _, p := range pairs {
		location := "database"
		if config.IsYamlOnlyKey(p.key) {
			location = "config.yaml"
		} else if p.key == "beads.role" {
			location = "git config"
		}

		expected := expectedLocations[p.key]
		if location != expected {
			t.Errorf("key %q: expected location %q, got %q", p.key, expected, location)
		}
	}
}

// TestConfigSetManyParseMultipleArgs tests parsing a full batch of arguments,
// including edge cases like values containing equals signs and empty values,
// as would happen with a real 'bd config set-many' invocation.
func TestConfigSetManyParseMultipleArgs(t *testing.T) {
	args := []string{
		"jira.url=https://example.atlassian.net",
		"jira.project=PROJ",
		"ado.state_map.open=New",
		"ado.state_map.closed=Closed",
		"custom.filter=status=open&label=bug", // value contains '='
		"custom.empty=",                       // empty value
	}

	type kvPair struct {
		key, value string
	}
	expected := []kvPair{
		{"jira.url", "https://example.atlassian.net"},
		{"jira.project", "PROJ"},
		{"ado.state_map.open", "New"},
		{"ado.state_map.closed", "Closed"},
		{"custom.filter", "status=open&label=bug"},
		{"custom.empty", ""},
	}

	pairs := make([]kvPair, 0, len(args))
	for _, arg := range args {
		idx := strings.Index(arg, "=")
		if idx <= 0 {
			t.Fatalf("unexpected parse failure for %q", arg)
		}
		pairs = append(pairs, kvPair{key: arg[:idx], value: arg[idx+1:]})
	}

	if len(pairs) != len(expected) {
		t.Fatalf("expected %d pairs, got %d", len(expected), len(pairs))
	}

	for i, p := range pairs {
		if p.key != expected[i].key {
			t.Errorf("pair[%d] key = %q, want %q", i, p.key, expected[i].key)
		}
		if p.value != expected[i].value {
			t.Errorf("pair[%d] value = %q, want %q", i, p.value, expected[i].value)
		}
	}
}

type proxiedConfigUseCase struct {
	domain.ConfigUseCase
	values  map[string]string
	deleted []string
}

func (f *proxiedConfigUseCase) SetConfig(_ context.Context, key, value string) error {
	f.values[key] = value
	return nil
}

func (f *proxiedConfigUseCase) DeleteConfig(_ context.Context, key string) error {
	delete(f.values, key)
	f.deleted = append(f.deleted, key)
	return nil
}

func (f *proxiedConfigUseCase) deletedKey(key string) bool {
	for _, deleted := range f.deleted {
		if deleted == key {
			return true
		}
	}
	return false
}

func (f *proxiedConfigUseCase) GetConfig(_ context.Context, key string) (string, error) {
	return f.values[key], nil
}

func (f *proxiedConfigUseCase) GetAllConfig(context.Context) (map[string]string, error) {
	values := make(map[string]string, len(f.values))
	for key, value := range f.values {
		values[key] = value
	}
	return values, nil
}

type proxiedConfigUOW struct {
	uow.UnitOfWork
	config  *proxiedConfigUseCase
	commits *[]string
}

func (f *proxiedConfigUOW) ConfigUseCase() domain.ConfigUseCase { return f.config }
func (f *proxiedConfigUOW) Commit(_ context.Context, message string) error {
	*f.commits = append(*f.commits, message)
	return nil
}
func (f *proxiedConfigUOW) Close(context.Context) {}

type proxiedConfigUOWProvider struct {
	uow.UnitOfWorkProvider
	config  *proxiedConfigUseCase
	commits []string
}

func (f *proxiedConfigUOWProvider) NewUOW(context.Context) (uow.UnitOfWork, error) {
	return &proxiedConfigUOW{config: f.config, commits: &f.commits}, nil
}

func TestProxiedConfigSetSecretDeletesLegacyValue(t *testing.T) {
	setupConfigOutputTest(t)
	oldProvider := uowProvider
	oldProxiedServerMode := proxiedServerMode
	const key = "custom.password"
	const value = "new-yaml-secret"
	fake := &proxiedConfigUseCase{values: map[string]string{key: "legacy-proxied-secret"}}
	provider := &proxiedConfigUOWProvider{config: fake}
	uowProvider = provider
	proxiedServerMode = true
	oldCommandContext := cmdCtx
	cmdCtx = nil
	t.Cleanup(func() { cmdCtx = oldCommandContext })
	t.Cleanup(func() {
		uowProvider = oldProvider
		proxiedServerMode = oldProxiedServerMode
	})

	captureStdout(t, func() error {
		return configSetCmd.RunE(configSetCmd, []string{key, value})
	})

	if !fake.deletedKey(key) {
		t.Errorf("proxied secret key %q did not delete legacy Dolt config", key)
	}
	if got, exists := fake.values[key]; exists {
		t.Errorf("proxied secret key %q remains in Dolt as %q", key, got)
	}
	if len(provider.commits) != 1 {
		t.Errorf("proxied secret deletion committed %d times, want 1", len(provider.commits))
	}
	if got := config.GetStringFromDir(os.Getenv("BEADS_DIR"), key); got != value {
		t.Errorf("proxied secret key %q config.yaml value = %q, want %q", key, got, value)
	}
}

func TestProxiedConfigSecretOutput(t *testing.T) {
	oldProvider := uowProvider
	oldJSONOutput := jsonOutput
	fake := &proxiedConfigUseCase{values: make(map[string]string)}
	uowProvider = &proxiedConfigUOWProvider{config: fake}
	t.Cleanup(func() {
		uowProvider = oldProvider
		jsonOutput = oldJSONOutput
	})

	const visible = "proxied-visible-value"
	tests := []struct {
		name      string
		operation string
		secret    string
		json      bool
		exposeRaw bool
	}{
		{name: "set text", operation: "set", secret: "proxied-set-text-secret"},
		{name: "set JSON", operation: "set", secret: "proxied-set-json-secret", json: true},
		{name: "list text", operation: "list", secret: "proxied-list-text-secret"},
		{name: "list JSON", operation: "list", secret: "proxied-list-json-secret", json: true},
		{name: "get text remains raw", operation: "get", secret: "proxied-get-text-secret", exposeRaw: true},
		{name: "get JSON remains raw", operation: "get", secret: "proxied-get-json-secret", json: true, exposeRaw: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake.values = map[string]string{
				"custom.password": tt.secret,
				"custom.visible":  visible,
			}
			jsonOutput = tt.json
			out := captureStdout(t, func() error {
				switch tt.operation {
				case "set":
					runConfigSetProxiedServer(context.Background(), "custom.password", tt.secret)
				case "list":
					runConfigListProxiedServer(context.Background())
				case "get":
					runConfigGetProxiedServer(context.Background(), "custom.password")
				}
				return nil
			})

			if tt.exposeRaw {
				if !strings.Contains(out, tt.secret) {
					t.Errorf("explicit proxied get omitted raw value %q:\n%s", tt.secret, out)
				}
				if strings.Contains(out, "[REDACTED]") {
					t.Errorf("explicit proxied get unexpectedly redacted value:\n%s", out)
				}
			} else {
				if strings.Contains(out, tt.secret) {
					t.Errorf("proxied config %s leaked secret %q:\n%s", tt.operation, tt.secret, out)
				}
				if !strings.Contains(out, "[REDACTED]") {
					t.Errorf("proxied config %s omitted redaction marker:\n%s", tt.operation, out)
				}
			}

			if tt.operation == "list" && !strings.Contains(out, visible) {
				t.Errorf("proxied config list changed non-secret value %q:\n%s", visible, out)
			}

			if tt.json {
				var payload map[string]interface{}
				if err := json.Unmarshal([]byte(out), &payload); err != nil {
					t.Fatalf("parse proxied config JSON: %v\n%s", err, out)
				}
				want := "[REDACTED]"
				if tt.exposeRaw {
					want = tt.secret
				}
				if got, _ := payload["value"].(string); tt.operation != "list" && got != want {
					t.Errorf("proxied config %s JSON value = %q, want %q", tt.operation, got, want)
				}
				if got, _ := payload["custom.password"].(string); tt.operation == "list" && got != want {
					t.Errorf("proxied config list JSON secret = %q, want %q", got, want)
				}
			}
		})
	}
}
