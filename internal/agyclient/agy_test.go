package agyclient

import (
	"errors"
	"io"
	"os/exec"
	"strings"
	"testing"
)

// TestCallFallbackOrder pins the order: agy twice, then the backup command.
func TestCallFallbackOrder(t *testing.T) {
	var seen []string
	restore := run
	t.Cleanup(func() { run = restore })
	run = func(cmd *exec.Cmd) (string, error) {
		seen = append(seen, strings.Join(cmd.Args, " "))
		if cmd.Args[0] == "agy" {
			return "", errors.New("boom")
		}
		stdin, err := io.ReadAll(cmd.Stdin)
		if err != nil {
			t.Fatal(err)
		}
		return "from backup: " + string(stdin), nil
	}

	out, err := Call("PROMPT", "some-model", "backup-cli")
	if err != nil || out != "from backup: PROMPT" {
		t.Fatalf("out=%q err=%v", out, err)
	}
	if len(seen) != 3 || seen[0] != "agy --print PROMPT" || seen[1] != seen[0] || seen[2] != "sh -c backup-cli" {
		t.Fatalf("call order: %v", seen)
	}
}

func TestCallReturnsFirstSuccess(t *testing.T) {
	calls := 0
	restore := run
	t.Cleanup(func() { run = restore })
	run = func(*exec.Cmd) (string, error) {
		calls++
		return "ok", nil
	}
	if out, err := Call("p", "", ""); err != nil || out != "ok" || calls != 1 {
		t.Fatalf("out=%q err=%v calls=%d", out, err, calls)
	}
}

// TestCallNoBackupIsAnError: with no backup command anywhere, a dead agy is an
// error naming the env var, not an empty string a caller might write down.
func TestCallNoBackupIsAnError(t *testing.T) {
	restore := run
	t.Cleanup(func() { run = restore })
	run = func(*exec.Cmd) (string, error) { return "", errors.New("boom") }
	t.Setenv(BackupCmdEnv, "")

	out, err := Call("p", "", "")
	if err == nil || out != "" || !strings.Contains(err.Error(), BackupCmdEnv) {
		t.Fatalf("out=%q err=%v", out, err)
	}
}
