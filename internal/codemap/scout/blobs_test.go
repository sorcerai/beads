package scout

import (
	"os/exec"
	"testing"
)

func TestBlobHashesFromGit(t *testing.T) {
	root := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v: %s", err, out)
		}
	}
	run("init", "-q")
	mustWrite(t, root, "a.go", "package a\n")
	run("add", "a.go")
	hashes, err := BlobHashes(root)
	if err != nil || len(hashes["a.go"]) != 40 {
		t.Fatalf("got %v %v", hashes, err)
	}
	dirs := PackageDirsFor([]string{"cmd/bd/x.go", "cmd/bd/y.go", "internal/z/w.go"})
	if len(dirs) != 2 || dirs[0] != "cmd/bd" || dirs[1] != "internal/z" {
		t.Fatalf("dirs: %v", dirs)
	}
}
