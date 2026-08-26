package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
)

func TestPostCloseHookRejectsUnsafePathsAndPermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("post-close hooks require POSIX executable and permission semantics")
	}

	tests := []struct {
		name      string
		kind      string
		shouldRun bool
	}{
		{name: "owner controlled executable", kind: "safe", shouldRun: true},
		{name: "hook file symlink", kind: "hook-symlink"},
		{name: "hooks directory symlink", kind: "directory-symlink"},
		{name: "hook group writable", kind: "hook-group-writable"},
		{name: "hook other writable", kind: "hook-other-writable"},
		{name: "hooks directory group writable", kind: "directory-group-writable"},
		{name: "hooks directory other writable", kind: "directory-other-writable"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := t.TempDir()
			beadsDir := filepath.Join(repo, ".beads")
			if err := os.Mkdir(beadsDir, 0o700); err != nil {
				t.Fatalf("create .beads: %v", err)
			}
			if err := os.WriteFile(filepath.Join(beadsDir, "config.yaml"), nil, 0o600); err != nil {
				t.Fatalf("create config.yaml: %v", err)
			}
			hooksDir := filepath.Join(beadsDir, "hooks")
			hookPath := filepath.Join(hooksDir, postCloseHookName)
			marker := filepath.Join(repo, "hook-ran")
			t.Setenv("BEADS_DIR", "")
			quotedMarker := "'" + strings.ReplaceAll(marker, "'", "'\"'\"'") + "'"
			t.Chdir(repo)

			writeHook := func(path string) {
				t.Helper()
				body := "#!/bin/sh\nprintf '%s' \"$1\" > " + quotedMarker + "\n"
				if err := os.WriteFile(path, []byte(body), 0o700); err != nil {
					t.Fatalf("write hook: %v", err)
				}
			}

			switch tt.kind {
			case "directory-symlink":
				outsideHooks := filepath.Join(t.TempDir(), "hooks")
				if err := os.Mkdir(outsideHooks, 0o700); err != nil {
					t.Fatalf("create outside hooks directory: %v", err)
				}
				writeHook(filepath.Join(outsideHooks, postCloseHookName))
				if err := os.Symlink(outsideHooks, hooksDir); err != nil {
					t.Fatalf("symlink hooks directory: %v", err)
				}
			default:
				if err := os.Mkdir(hooksDir, 0o700); err != nil {
					t.Fatalf("create hooks directory: %v", err)
				}
				if tt.kind == "hook-symlink" {
					target := filepath.Join(t.TempDir(), postCloseHookName)
					writeHook(target)
					if err := os.Symlink(target, hookPath); err != nil {
						t.Fatalf("symlink hook: %v", err)
					}
				} else {
					writeHook(hookPath)
				}
			}

			switch tt.kind {
			case "hook-group-writable":
				if err := os.Chmod(hookPath, 0o720); err != nil {
					t.Fatalf("chmod hook: %v", err)
				}
			case "hook-other-writable":
				if err := os.Chmod(hookPath, 0o702); err != nil {
					t.Fatalf("chmod hook: %v", err)
				}
			case "directory-group-writable":
				if err := os.Chmod(hooksDir, 0o770); err != nil {
					t.Fatalf("chmod hooks directory: %v", err)
				}
			case "directory-other-writable":
				if err := os.Chmod(hooksDir, 0o707); err != nil {
					t.Fatalf("chmod hooks directory: %v", err)
				}
			}

			resolved := resolvePostCloseHookPathForStore(nil)
			if tt.shouldRun {
				resolvedInfo, resolvedErr := os.Stat(resolved)
				hookInfo, hookErr := os.Stat(hookPath)
				if resolvedErr != nil || hookErr != nil || !os.SameFile(resolvedInfo, hookInfo) {
					t.Fatalf("safe hook resolved to %q instead of %q (resolved err=%v, hook err=%v)", resolved, hookPath, resolvedErr, hookErr)
				}
			} else if resolved != "" {
				t.Errorf("unsafe hook resolved to %q, want rejection", resolved)
			}

			runPostCloseHookAt(context.Background(), hookPath, []string{"issue-123"})
			data, err := os.ReadFile(marker)
			if tt.shouldRun {
				if err != nil {
					t.Fatalf("safe hook did not run: %v", err)
				}
				if string(data) != "issue-123" {
					t.Errorf("safe hook received %q, want issue-123", data)
				}
			} else if !os.IsNotExist(err) {
				t.Errorf("unsafe hook executed; marker read error=%v content=%q", err, data)
			}
		})
	}
}

func TestPostCloseHookSnapshotSurvivesSourceReplacement(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("post-close hooks require POSIX executable and permission semantics")
	}

	repo := t.TempDir()
	hooksDir := filepath.Join(repo, ".beads", "hooks")
	if err := os.MkdirAll(hooksDir, 0o700); err != nil {
		t.Fatalf("create hooks directory: %v", err)
	}
	hookPath := filepath.Join(hooksDir, postCloseHookName)
	marker := filepath.Join(repo, "hook-ran")
	writeHook := func(value string) {
		t.Helper()
		body := "#!/bin/sh\nprintf '%s' " + value + " > " + marker + "\n"
		if err := os.WriteFile(hookPath, []byte(body), 0o700); err != nil {
			t.Fatalf("write hook: %v", err)
		}
	}

	writeHook("trusted")
	snapshotPath, cleanup, err := snapshotPostCloseHook(hookPath)
	if err != nil {
		t.Fatalf("snapshot hook: %v", err)
	}
	defer cleanup()

	writeHook("replacement")
	cmd := exec.Command(snapshotPath)
	cmd.Dir = repo
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("run snapshot: %v: %s", err, output)
	}
	data, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("read marker: %v", err)
	}
	if got := string(data); got != "trusted" {
		t.Fatalf("snapshot ran %q, want trusted", got)
	}
}

func TestPostCloseHookSanitizesEnvironment(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("post-close hooks require POSIX executable and permission semantics")
	}

	repo := t.TempDir()
	hooksDir := filepath.Join(repo, ".beads", "hooks")
	if err := os.MkdirAll(hooksDir, 0o700); err != nil {
		t.Fatalf("create hooks directory: %v", err)
	}
	if err := os.Chmod(hooksDir, 0o700); err != nil {
		t.Fatalf("chmod hooks directory: %v", err)
	}
	hookPath := filepath.Join(hooksDir, postCloseHookName)
	marker := filepath.Join(repo, "hook-env")
	quotedMarker := "'" + strings.ReplaceAll(marker, "'", "'\"'\"'") + "'"
	body := "#!/bin/sh\nprintf 'token=%s\\npath=%s\\nno_docs=%s\\n' \"${GITHUB_TOKEN-}\" \"${PATH-}\" \"${BD_NO_DOCS-}\" > " + quotedMarker + "\n"
	if err := os.WriteFile(hookPath, []byte(body), 0o700); err != nil {
		t.Fatalf("write hook: %v", err)
	}
	if err := os.Chmod(hookPath, 0o700); err != nil {
		t.Fatalf("chmod hook: %v", err)
	}

	const credential = "github-token-must-not-reach-hook"
	t.Setenv("GITHUB_TOKEN", credential)
	t.Setenv("PATH", "/usr/bin:/bin")
	t.Setenv("BD_NO_DOCS", "1")
	runPostCloseHookAt(context.Background(), hookPath, []string{"issue-123"})

	data, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("owner-controlled hook did not run: %v", err)
	}
	out := string(data)
	if strings.Contains(out, credential) || !strings.Contains(out, "token=\n") {
		t.Errorf("post-close hook received credential environment variable:\n%s", out)
	}
	if !strings.Contains(out, "path=/usr/bin:/bin\n") {
		t.Errorf("post-close hook lost allowlisted PATH:\n%s", out)
	}
	if !strings.Contains(out, "no_docs=1\n") {
		t.Errorf("post-close hook lost allowlisted BD_NO_DOCS:\n%s", out)
	}
}

func TestSeedPostCloseHookMutationSafety(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("post-close hooks require POSIX symlink semantics")
	}

	tests := []struct {
		name string
		kind string
		safe bool
	}{
		{name: "ordinary missing hook", kind: "safe", safe: true},
		{name: "hooks directory symlink", kind: "directory-symlink"},
		{name: "hook file symlink", kind: "hook-symlink"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := t.TempDir()
			beadsDir := filepath.Join(repo, ".beads")
			if err := os.Mkdir(beadsDir, 0o700); err != nil {
				t.Fatalf("create .beads: %v", err)
			}
			if err := os.WriteFile(filepath.Join(beadsDir, "config.yaml"), nil, 0o600); err != nil {
				t.Fatalf("create config.yaml: %v", err)
			}
			t.Setenv("BEADS_DIR", beadsDir)
			t.Chdir(repo)

			hooksDir := filepath.Join(beadsDir, "hooks")
			hookPath := filepath.Join(hooksDir, postCloseHookName)
			outsideTarget := ""
			switch tt.kind {
			case "directory-symlink":
				outsideHooks := filepath.Join(t.TempDir(), "hooks")
				if err := os.Mkdir(outsideHooks, 0o700); err != nil {
					t.Fatalf("create outside hooks directory: %v", err)
				}
				outsideTarget = filepath.Join(outsideHooks, postCloseHookName)
				if err := os.Symlink(outsideHooks, hooksDir); err != nil {
					t.Fatalf("symlink hooks directory: %v", err)
				}
			case "hook-symlink":
				if err := os.Mkdir(hooksDir, 0o700); err != nil {
					t.Fatalf("create hooks directory: %v", err)
				}
				outsideTarget = filepath.Join(t.TempDir(), postCloseHookName)
				if err := os.Symlink(outsideTarget, hookPath); err != nil {
					t.Fatalf("symlink hook: %v", err)
				}
			}

			created := seedPostCloseHook(repo)
			if tt.safe {
				if !created {
					t.Fatal("seedPostCloseHook did not create ordinary owner-controlled hook")
				}
				data, err := os.ReadFile(hookPath)
				if err != nil {
					t.Fatalf("read seeded hook: %v", err)
				}
				if string(data) != postCloseHookTemplate {
					t.Error("seeded hook content changed")
				}
				return
			}

			if created {
				t.Errorf("seedPostCloseHook reported creation through unsafe %s", tt.kind)
			}
			if _, err := os.Lstat(outsideTarget); !os.IsNotExist(err) {
				t.Errorf("seedPostCloseHook created outside target %s", outsideTarget)
			}
		})
	}
}

func TestPostCloseHookTemplateRequiresArchCheckOptIn(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("post-close hook template is a POSIX shell script")
	}

	repo := t.TempDir()
	hooksDir := filepath.Join(repo, ".beads", "hooks")
	if err := os.MkdirAll(hooksDir, 0o700); err != nil {
		t.Fatalf("create hooks directory: %v", err)
	}
	hookPath := filepath.Join(hooksDir, postCloseHookName)
	if err := os.WriteFile(hookPath, []byte(postCloseHookTemplate), 0o700); err != nil {
		t.Fatalf("write generated hook: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, "ARCH.md"), []byte("# Architecture\n"), 0o600); err != nil {
		t.Fatalf("write ARCH.md: %v", err)
	}
	scriptsDir := filepath.Join(repo, "scripts")
	if err := os.Mkdir(scriptsDir, 0o700); err != nil {
		t.Fatalf("create scripts directory: %v", err)
	}
	marker := filepath.Join(repo, "repository-script-ran")
	quotedMarker := "'" + strings.ReplaceAll(marker, "'", "'\"'\"'") + "'"
	malicious := "#!/bin/sh\nprintf ran > " + quotedMarker + "\n"
	if err := os.WriteFile(filepath.Join(scriptsDir, "arch-check.sh"), []byte(malicious), 0o700); err != nil {
		t.Fatalf("write repository-controlled arch check: %v", err)
	}
	t.Chdir(repo)
	t.Setenv("BD_ARCH_REVIEW", "")

	tests := []struct {
		name       string
		enabled    string
		wantMarker bool
	}{
		{name: "disabled by default"},
		{name: "explicitly enabled", enabled: "1", wantMarker: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := os.Remove(marker); err != nil && !os.IsNotExist(err) {
				t.Fatalf("remove prior marker: %v", err)
			}
			t.Setenv("BD_ARCH_CHECK", tt.enabled)
			runPostCloseHookAt(context.Background(), hookPath, []string{"issue-123"})

			_, err := os.Stat(marker)
			if tt.wantMarker && err != nil {
				t.Errorf("BD_ARCH_CHECK=1 did not run repository arch-check: %v", err)
			}
			if !tt.wantMarker && !os.IsNotExist(err) {
				t.Error("repository arch-check ran without explicit BD_ARCH_CHECK=1")
			}
		})
	}
}

const legacyPostCloseTier1Fixture = `# --- Tier 1: deterministic check (free, 0 tokens) ---
if [ -x ./scripts/arch-check.sh ]; then
  ./scripts/arch-check.sh || echo "⚠  see arch-check output above (advisory)" >&2
fi
`

func TestRunPostCloseHookAtRefusesLegacyUnconditionalArchCheck(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("post-close hook is a POSIX shell script")
	}
	repo := t.TempDir()
	hooksDir := filepath.Join(repo, ".beads", "hooks")
	if err := os.MkdirAll(hooksDir, 0o700); err != nil {
		t.Fatalf("create hooks directory: %v", err)
	}
	hookPath := filepath.Join(hooksDir, postCloseHookName)
	legacyHook := "#!/bin/sh\nset -eu\n" + legacyPostCloseTier1Fixture + "exit 0\n"
	if err := os.WriteFile(hookPath, []byte(legacyHook), 0o700); err != nil {
		t.Fatalf("write legacy hook: %v", err)
	}
	marker := filepath.Join(repo, "legacy-arch-check-ran")
	quotedMarker := "'" + strings.ReplaceAll(marker, "'", "'\"'\"'") + "'"
	scriptsDir := filepath.Join(repo, "scripts")
	if err := os.Mkdir(scriptsDir, 0o700); err != nil {
		t.Fatalf("create scripts directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(scriptsDir, "arch-check.sh"), []byte("#!/bin/sh\nprintf ran > "+quotedMarker+"\n"), 0o700); err != nil {
		t.Fatalf("write repository arch check: %v", err)
	}
	t.Chdir(repo)
	t.Setenv("BD_ARCH_CHECK", "")
	runPostCloseHookAt(context.Background(), hookPath, []string{"issue-123"})
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Error("legacy unconditional arch-check executed without BD_ARCH_CHECK=1")
	}
}

func TestSeedPostCloseHookUpgradesLegacyManagedArchCheck(t *testing.T) {
	repo := t.TempDir()
	beadsDir := filepath.Join(repo, ".beads")
	hooksDir := filepath.Join(beadsDir, "hooks")
	if err := os.MkdirAll(hooksDir, 0o700); err != nil {
		t.Fatalf("create hooks directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "config.yaml"), nil, 0o600); err != nil {
		t.Fatalf("create config.yaml: %v", err)
	}
	const prefix = "#!/bin/sh\n# unrelated before\n"
	const suffix = "# unrelated after\nexit 0\n"
	hookPath := filepath.Join(hooksDir, postCloseHookName)
	if err := os.WriteFile(hookPath, []byte(prefix+legacyPostCloseTier1Fixture+suffix), 0o700); err != nil {
		t.Fatalf("write legacy generated hook: %v", err)
	}
	t.Setenv("BEADS_DIR", beadsDir)
	t.Chdir(repo)
	seedPostCloseHook(repo)

	data, err := os.ReadFile(hookPath)
	if err != nil {
		t.Fatalf("read upgraded hook: %v", err)
	}
	content := string(data)
	if !strings.HasPrefix(content, prefix) || !strings.HasSuffix(content, suffix) {
		t.Errorf("legacy upgrade changed unrelated hook content:\n%s", content)
	}
	if strings.Contains(content, legacyPostCloseTier1Fixture) {
		t.Error("seedPostCloseHook left legacy unconditional managed block in place")
	}
	if !strings.Contains(content, `if [ "${BD_ARCH_CHECK:-0}" = "1" ] && [ -x ./scripts/arch-check.sh ]; then`) {
		t.Errorf("seedPostCloseHook did not install gated managed block:\n%s", content)
	}
}

func TestBeadsDirSymlinkRefusesPostCloseHookOperations(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("post-close hooks require POSIX symlink semantics")
	}

	setup := func(t *testing.T) (repo, outsideBeads, hooksDir string) {
		t.Helper()
		repo = t.TempDir()
		outsideBeads = filepath.Join(t.TempDir(), "outside-beads")
		hooksDir = filepath.Join(outsideBeads, "hooks")
		if err := os.MkdirAll(hooksDir, 0o700); err != nil {
			t.Fatalf("create outside hooks directory: %v", err)
		}
		if err := os.WriteFile(filepath.Join(outsideBeads, "config.yaml"), nil, 0o600); err != nil {
			t.Fatalf("create outside config.yaml: %v", err)
		}
		if err := os.Symlink(outsideBeads, filepath.Join(repo, ".beads")); err != nil {
			t.Fatalf("symlink .beads: %v", err)
		}
		t.Setenv("BEADS_DIR", "")
		t.Chdir(repo)
		return repo, outsideBeads, hooksDir
	}

	t.Run("execution", func(t *testing.T) {
		repo, _, hooksDir := setup(t)
		marker := filepath.Join(t.TempDir(), "outside-hook-ran")
		quotedMarker := "'" + strings.ReplaceAll(marker, "'", "'\"'\"'") + "'"
		hookPath := filepath.Join(hooksDir, postCloseHookName)
		if err := os.WriteFile(hookPath, []byte("#!/bin/sh\nprintf ran > "+quotedMarker+"\n"), 0o700); err != nil {
			t.Fatalf("write outside hook: %v", err)
		}
		runPostCloseHookAt(context.Background(), filepath.Join(repo, ".beads", "hooks", postCloseHookName), []string{"issue-123"})
		if _, err := os.Stat(marker); !os.IsNotExist(err) {
			t.Error("runPostCloseHookAt executed through symlinked .beads")
		}
	})

	t.Run("seed", func(t *testing.T) {
		repo, _, hooksDir := setup(t)
		outsideHook := filepath.Join(hooksDir, postCloseHookName)
		if seedPostCloseHook(repo) {
			t.Error("seedPostCloseHook created hook through symlinked .beads")
		}
		if _, err := os.Lstat(outsideHook); !os.IsNotExist(err) {
			t.Errorf("seedPostCloseHook modified outside target %s", outsideHook)
		}
	})

	t.Run("wire", func(t *testing.T) {
		repo, _, hooksDir := setup(t)
		outsideHook := filepath.Join(hooksDir, postCloseHookName)
		const sentinel = "outside hook sentinel\n"
		if err := os.WriteFile(outsideHook, []byte(sentinel), 0o700); err != nil {
			t.Fatalf("write outside sentinel: %v", err)
		}
		created, wired, err := wireDocsHook(repo)
		if err == nil || created || wired {
			t.Errorf("wireDocsHook did not refuse symlinked .beads: created=%v wired=%v err=%v", created, wired, err)
		}
		data, readErr := os.ReadFile(outsideHook)
		if readErr != nil {
			t.Fatalf("read outside sentinel: %v", readErr)
		}
		if string(data) != sentinel {
			t.Errorf("wireDocsHook modified outside target:\n%s", data)
		}
	})
}

func TestRunPostCloseHookAtUsesHookWorkspace(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("post-close hook is a POSIX shell script")
	}
	workspaceA := t.TempDir()
	workspaceB := t.TempDir()
	hooksDir := filepath.Join(workspaceA, ".beads", "hooks")
	if err := os.MkdirAll(hooksDir, 0o700); err != nil {
		t.Fatalf("create workspace A hooks: %v", err)
	}
	if err := os.Mkdir(filepath.Join(workspaceB, ".beads"), 0o700); err != nil {
		t.Fatalf("create workspace B .beads: %v", err)
	}
	marker := filepath.Join(t.TempDir(), "hook-workspace")
	quotedMarker := "'" + strings.ReplaceAll(marker, "'", "'\"'\"'") + "'"
	hookPath := filepath.Join(hooksDir, postCloseHookName)
	body := "#!/bin/sh\nprintf 'cwd=%s\\nbeads=%s\\n' \"$(pwd -P)\" \"${BEADS_DIR-}\" > " + quotedMarker + "\n"
	if err := os.WriteFile(hookPath, []byte(body), 0o700); err != nil {
		t.Fatalf("write workspace A hook: %v", err)
	}
	t.Setenv("BEADS_DIR", filepath.Join(workspaceB, ".beads"))
	t.Chdir(workspaceB)
	runPostCloseHookAt(context.Background(), hookPath, []string{"issue-123"})

	data, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("workspace A hook did not run: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 2 {
		t.Fatalf("unexpected hook workspace output %q", data)
	}
	gotCWD := strings.TrimPrefix(lines[0], "cwd=")
	gotBeads := strings.TrimPrefix(lines[1], "beads=")
	wantCWD, _ := filepath.EvalSymlinks(workspaceA)
	wantBeads, _ := filepath.EvalSymlinks(filepath.Join(workspaceA, ".beads"))
	gotCWD, _ = filepath.EvalSymlinks(gotCWD)
	gotBeads, _ = filepath.EvalSymlinks(gotBeads)
	if gotCWD != wantCWD {
		t.Errorf("hook cwd = %q, want workspace A %q", gotCWD, wantCWD)
	}
	if gotBeads != wantBeads {
		t.Errorf("hook BEADS_DIR = %q, want workspace A %q", gotBeads, wantBeads)
	}
}

func TestPostCloseHookRejectsGitTrackedProvenance(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("post-close hooks require POSIX executable semantics")
	}
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not available")
	}

	tests := []struct {
		name    string
		tracked bool
	}{
		{name: "tracked hook is rejected", tracked: true},
		{name: "untracked hook runs"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := t.TempDir()
			if output, initErr := exec.Command(gitPath, "init", repo).CombinedOutput(); initErr != nil {
				t.Fatalf("git init: %v\n%s", initErr, output)
			}
			beadsDir := filepath.Join(repo, ".beads")
			hooksDir := filepath.Join(beadsDir, "hooks")
			if err := os.MkdirAll(hooksDir, 0o700); err != nil {
				t.Fatalf("create hooks directory: %v", err)
			}
			if err := os.WriteFile(filepath.Join(beadsDir, "config.yaml"), nil, 0o600); err != nil {
				t.Fatalf("create config.yaml: %v", err)
			}
			marker := filepath.Join(repo, "hook-ran")
			quotedMarker := "'" + strings.ReplaceAll(marker, "'", "'\"'\"'") + "'"
			hookPath := filepath.Join(hooksDir, postCloseHookName)
			hook := "#!/bin/sh\nprintf '%s' \"$1\" > " + quotedMarker + "\n"
			if err := os.WriteFile(hookPath, []byte(hook), 0o700); err != nil {
				t.Fatalf("write hook: %v", err)
			}
			if tt.tracked {
				cmd := exec.Command(gitPath, "add", "-f", ".beads/hooks/post-close")
				cmd.Dir = repo
				if output, addErr := cmd.CombinedOutput(); addErr != nil {
					t.Fatalf("git add tracked hook: %v\n%s", addErr, output)
				}
			}

			t.Setenv("BEADS_DIR", "")
			t.Chdir(repo)
			resolved := resolvePostCloseHookPathForStore(nil)
			if tt.tracked && resolved != "" {
				t.Errorf("git-tracked post-close hook resolved to %q, want rejection", resolved)
			}
			if !tt.tracked && resolved == "" {
				t.Error("untracked owner-controlled post-close hook did not resolve")
			}

			runPostCloseHookAt(context.Background(), hookPath, []string{"issue-123"})
			data, readErr := os.ReadFile(marker)
			if tt.tracked {
				if !os.IsNotExist(readErr) {
					t.Errorf("git-tracked post-close hook executed: err=%v content=%q", readErr, data)
				}
				return
			}
			if readErr != nil {
				t.Fatalf("untracked post-close hook did not run: %v", readErr)
			}
			if string(data) != "issue-123" {
				t.Errorf("untracked post-close hook received %q, want issue-123", data)
			}
		})
	}
}

type postCloseLocatorStore struct {
	storage.DoltStorage
	path string
}

func (s *postCloseLocatorStore) Path() string   { return s.path }
func (s *postCloseLocatorStore) CLIDir() string { return filepath.Dir(s.path) }

func TestResolvePostCloseHookDoesNotFallbackFromUnsafeStoreHook(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("post-close hook is a POSIX shell script")
	}

	workspaceA := t.TempDir()
	beadsA := filepath.Join(workspaceA, ".beads")
	hooksA := filepath.Join(beadsA, "hooks")
	if err := os.MkdirAll(hooksA, 0o700); err != nil {
		t.Fatalf("create workspace A hooks: %v", err)
	}
	legacyHook := "#!/bin/sh\nset -eu\n" + legacyPostCloseTier1Fixture + "exit 0\n"
	if err := os.WriteFile(filepath.Join(hooksA, postCloseHookName), []byte(legacyHook), 0o700); err != nil {
		t.Fatalf("write unsafe workspace A hook: %v", err)
	}

	workspaceB := t.TempDir()
	beadsB := filepath.Join(workspaceB, ".beads")
	hooksB := filepath.Join(beadsB, "hooks")
	if err := os.MkdirAll(hooksB, 0o700); err != nil {
		t.Fatalf("create workspace B hooks: %v", err)
	}
	if err := os.WriteFile(filepath.Join(beadsB, "config.yaml"), nil, 0o600); err != nil {
		t.Fatalf("create workspace B config: %v", err)
	}
	marker := filepath.Join(workspaceB, "fallback-hook-ran")
	quotedMarker := "'" + strings.ReplaceAll(marker, "'", "'\"'\"'") + "'"
	safeHook := "#!/bin/sh\nprintf '%s' \"$1\" > " + quotedMarker + "\n"
	if err := os.WriteFile(filepath.Join(hooksB, postCloseHookName), []byte(safeHook), 0o700); err != nil {
		t.Fatalf("write safe workspace B hook: %v", err)
	}

	t.Setenv("BEADS_DIR", "")
	t.Chdir(workspaceB)
	storeA := &postCloseLocatorStore{path: filepath.Join(beadsA, "dolt")}
	if resolved := resolvePostCloseHookPathForStore(storeA); resolved != "" {
		t.Errorf("unsafe workspace A hook fell back to cwd hook %q, want no hook", resolved)
	}
	firePostCloseHook(context.Background(), storeA, []string{"issue-123"})
	if data, err := os.ReadFile(marker); !os.IsNotExist(err) {
		t.Errorf("firePostCloseHook executed workspace B fallback: err=%v content=%q", err, data)
	}
}
