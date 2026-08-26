package scripts_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

type graphifyRun struct {
	stdout string
	stderr string
	err    error
}
type graphifyFixture struct {
	repo  string
	cache string
	bin   string
}

func TestGraphifyRequiresExactVersion(t *testing.T) {
	fixture := newGraphifyFixture(t)
	run := fixture.run(t, "status")
	if graphifyExitCode(run.err) != 2 || !strings.Contains(run.stderr, "graphify 0.9.27 is required") {
		t.Fatalf("missing graphify: exit=%d stderr=%q", graphifyExitCode(run.err), run.stderr)
	}

	fixture.writeFake(t, "0.9.26")
	run = fixture.run(t, "status")
	if graphifyExitCode(run.err) != 2 || !strings.Contains(run.stderr, "graphify 0.9.27 is required") {
		t.Fatalf("wrong graphify version: exit=%d stderr=%q", graphifyExitCode(run.err), run.stderr)
	}
}

func TestGraphifyRefreshTracksVisibleWorktree(t *testing.T) {
	fixture := newGraphifyFixture(t)
	fixture.writeFake(t, "0.9.27")

	if run := fixture.run(t, "status"); run.err != nil || !strings.Contains(run.stdout, `"state":"missing"`) {
		t.Fatalf("missing status: err=%v stdout=%q stderr=%q", run.err, run.stdout, run.stderr)
	}
	if run := fixture.run(t, "refresh"); run.err != nil {
		t.Fatalf("refresh: %v\n%s", run.err, run.stderr)
	}
	if run := fixture.run(t, "status"); run.err != nil || !strings.Contains(run.stdout, `"state":"current"`) {
		t.Fatalf("current status: err=%v stdout=%q stderr=%q", run.err, run.stdout, run.stderr)
	}

	writeFile(t, filepath.Join(fixture.repo, "tracked.txt"), "changed\n")
	if run := fixture.run(t, "query", "tracked"); graphifyExitCode(run.err) != 2 || !strings.Contains(run.stderr, "./scripts/graphify refresh") {
		t.Fatalf("stale query: exit=%d stderr=%q", graphifyExitCode(run.err), run.stderr)
	}
}

func TestGraphifyFingerprintIncludesUntrackedButNotIgnoredFiles(t *testing.T) {
	fixture := newGraphifyFixture(t)
	fixture.writeFake(t, "0.9.27")
	writeFile(t, filepath.Join(fixture.repo, ".gitignore"), "ignored.txt\n")
	git(t, fixture.repo, "add", ".gitignore")
	git(t, fixture.repo, "commit", "-m", "ignore fixture")

	mustRefresh(t, fixture)
	writeFile(t, filepath.Join(fixture.repo, "ignored.txt"), "ignore me\n")
	if run := fixture.run(t, "status"); run.err != nil || !strings.Contains(run.stdout, `"state":"current"`) {
		t.Fatalf("ignored file made graph stale: %v %q", run.err, run.stdout)
	}
	writeFile(t, filepath.Join(fixture.repo, "new.txt"), "visible\n")
	if run := fixture.run(t, "status"); run.err != nil || !strings.Contains(run.stdout, `"state":"stale"`) {
		t.Fatalf("untracked file did not make graph stale: %v %q", run.err, run.stdout)
	}
}

func TestGraphifyRefreshPreservesPreviousGenerationOnFailure(t *testing.T) {
	fixture := newGraphifyFixture(t)
	fixture.writeFake(t, "0.9.27")
	mustRefresh(t, fixture)
	cacheBefore := readTree(t, fixture.cache)

	run := fixture.runEnv(t, []string{"FAKE_GRAPHIFY_MODE=fail"}, "refresh")
	if graphifyExitCode(run.err) != 41 {
		t.Fatalf("failed refresh exit = %d, stderr=%q", graphifyExitCode(run.err), run.stderr)
	}
	if run := fixture.run(t, "status"); run.err != nil || !strings.Contains(run.stdout, `"state":"current"`) {
		t.Fatalf("failed refresh lost previous graph: %v %q", run.err, run.stdout)
	}
	for _, name := range readTree(t, fixture.cache) {
		if strings.Contains(name, ".staging-") || strings.HasSuffix(name, ".tmp") || strings.HasSuffix(name, ".lock") {
			t.Fatalf("temporary cache path remained: %q (before %q)", name, cacheBefore)
		}
	}
}

func TestGraphifyRejectsSymlinkedCacheAndAllowsOnlyManagedQueries(t *testing.T) {
	fixture := newGraphifyFixture(t)
	fixture.writeFake(t, "0.9.27")
	root := fixture.cacheRoot(t)
	if err := os.MkdirAll(filepath.Dir(root), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), root); err != nil {
		t.Fatal(err)
	}
	if run := fixture.run(t, "status"); graphifyExitCode(run.err) != 2 || !strings.Contains(run.stderr, "unsafe managed directory") {
		t.Fatalf("symlinked cache was accepted: exit=%d stderr=%q", graphifyExitCode(run.err), run.stderr)
	}

	fixture = newGraphifyFixture(t)
	fixture.writeFake(t, "0.9.27")
	mustRefresh(t, fixture)
	if run := fixture.run(t, "query", "tracked"); run.err != nil || !strings.Contains(run.stdout, "delegated:query tracked --graph") {
		t.Fatalf("current query was not delegated safely: %v %q %q", run.err, run.stdout, run.stderr)
	}
	if run := fixture.run(t, "affected", "tracked.txt"); run.err != nil || !strings.Contains(run.stdout, "delegated:affected tracked.txt --graph") {
		t.Fatalf("current affected query was not delegated safely: %v %q %q", run.err, run.stdout, run.stderr)
	}
	if run := fixture.run(t, "query", "--graph"); graphifyExitCode(run.err) != 2 {
		t.Fatalf("graph override was accepted: exit=%d stderr=%q", graphifyExitCode(run.err), run.stderr)
	}
	if run := fixture.run(t, "affected", "../outside"); graphifyExitCode(run.err) != 2 || !strings.Contains(run.stderr, "inside the repository") {
		t.Fatalf("outside affected path was accepted: exit=%d stderr=%q", graphifyExitCode(run.err), run.stderr)
	}
}

func TestGraphifyRejectsSymlinkedCacheAncestor(t *testing.T) {
	fixture := newGraphifyFixture(t)
	fixture.writeFake(t, "0.9.27")
	root := fixture.cacheRoot(t)
	ancestor := filepath.Dir(root)
	if err := os.MkdirAll(filepath.Dir(ancestor), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), ancestor); err != nil {
		t.Fatal(err)
	}
	if run := fixture.run(t, "status"); graphifyExitCode(run.err) != 2 || !strings.Contains(run.stderr, "unsafe managed directory") {
		t.Fatalf("symlinked cache ancestor was accepted: exit=%d stderr=%q", graphifyExitCode(run.err), run.stderr)
	}
}

func (f graphifyFixture) cacheRoot(t *testing.T) string {
	t.Helper()
	repo, err := filepath.EvalSymlinks(f.repo)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(repo))
	return filepath.Join(f.cache, "omp", "graphify", hex.EncodeToString(sum[:]))
}

func TestGraphifyRefreshRejectsSymlinkedMetadata(t *testing.T) {
	fixture := newGraphifyFixture(t)
	fixture.writeFake(t, "0.9.27")
	mustRefresh(t, fixture)
	beforeGenerations := generationCount(t, fixture.cacheRoot(t))
	metadata := filepath.Join(fixture.cacheRoot(t), "current.json")
	if err := os.Remove(metadata); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), metadata); err != nil {
		t.Fatal(err)
	}
	if run := fixture.run(t, "refresh"); graphifyExitCode(run.err) != 2 || !strings.Contains(run.stderr, "unsafe managed metadata") {
		t.Fatalf("symlinked metadata was accepted: exit=%d stderr=%q", graphifyExitCode(run.err), run.stderr)
	}
	if afterGenerations := generationCount(t, fixture.cacheRoot(t)); afterGenerations != beforeGenerations {
		t.Fatalf("symlinked metadata left an orphan generation: before=%d after=%d", beforeGenerations, afterGenerations)
	}
}

func TestGraphifyMetadataRaceLeavesNoOrphanGeneration(t *testing.T) {
	fixture := newGraphifyFixture(t)
	fixture.writeFake(t, "0.9.27")
	mustRefresh(t, fixture)
	beforeGenerations := generationCount(t, fixture.cacheRoot(t))
	run := fixture.runEnv(t, []string{"FAKE_GRAPHIFY_MODE=metadata-race"}, "refresh")
	if graphifyExitCode(run.err) != 2 || !strings.Contains(run.stderr, "unsafe managed metadata") {
		t.Fatalf("metadata race was accepted: exit=%d stderr=%q", graphifyExitCode(run.err), run.stderr)
	}
	if afterGenerations := generationCount(t, fixture.cacheRoot(t)); afterGenerations != beforeGenerations {
		t.Fatalf("metadata race left an orphan generation: before=%d after=%d", beforeGenerations, afterGenerations)
	}
}

func TestGraphifyRejectsTraversalGenerationMetadata(t *testing.T) {
	fixture := newGraphifyFixture(t)
	fixture.writeFake(t, "0.9.27")
	mustRefresh(t, fixture)
	root := fixture.cacheRoot(t)
	metadataPath := filepath.Join(root, "current.json")
	contents, err := os.ReadFile(metadataPath)
	if err != nil {
		t.Fatal(err)
	}
	metadata := map[string]string{}
	if err := json.Unmarshal(contents, &metadata); err != nil {
		t.Fatal(err)
	}
	metadata["generation"] = "gen-/../../outside"
	if err := os.Mkdir(filepath.Join(root, "gen-"), 0o700); err != nil {
		t.Fatal(err)
	}
	outsideGraph := filepath.Join(filepath.Dir(root), "outside", "graphify-out", "graph.json")
	if err := os.MkdirAll(filepath.Dir(outsideGraph), 0o700); err != nil {
		t.Fatal(err)
	}
	writeFile(t, outsideGraph, "{}")
	contents, err = json.Marshal(metadata)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, metadataPath, string(contents))
	if run := fixture.run(t, "query", "tracked"); graphifyExitCode(run.err) != 2 || !strings.Contains(run.stderr, "./scripts/graphify refresh") {
		t.Fatalf("traversal generation was accepted: exit=%d stderr=%q", graphifyExitCode(run.err), run.stderr)
	}
}

func TestGraphifyRejectsSymlinkedOutputBeforePublication(t *testing.T) {
	fixture := newGraphifyFixture(t)
	fixture.writeFake(t, "0.9.27")
	run := fixture.runEnv(t, []string{"FAKE_GRAPHIFY_MODE=output-symlink"}, "refresh")
	if graphifyExitCode(run.err) != 2 || !strings.Contains(run.stderr, "unsafe staging output") {
		t.Fatalf("symlinked output was accepted: exit=%d stderr=%q", graphifyExitCode(run.err), run.stderr)
	}
	if generations := generationCount(t, fixture.cacheRoot(t)); generations != 0 {
		t.Fatalf("symlinked output was published: generations=%d", generations)
	}
}

func generationCount(t *testing.T, root string) int {
	t.Helper()
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, entry := range entries {
		if entry.IsDir() && strings.HasPrefix(entry.Name(), "gen-") {
			count++
		}
	}
	return count
}

func newGraphifyFixture(t *testing.T) graphifyFixture {
	t.Helper()
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	if err := os.Mkdir(repo, 0o700); err != nil {
		t.Fatal(err)
	}
	git(t, repo, "init")
	git(t, repo, "config", "user.email", "test@example.invalid")
	git(t, repo, "config", "user.name", "Test")
	writeFile(t, filepath.Join(repo, "tracked.txt"), "initial\n")
	git(t, repo, "add", "tracked.txt")
	git(t, repo, "commit", "-m", "initial")
	bin := filepath.Join(root, "bin")
	if err := os.Mkdir(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	return graphifyFixture{repo: repo, cache: filepath.Join(root, "cache"), bin: bin}
}

func (f graphifyFixture) writeFake(t *testing.T, version string) {
	t.Helper()
	writeFile(t, filepath.Join(f.bin, "graphify"), "#!/bin/sh\n"+
		"if [ \"$1\" = \"--version\" ]; then echo 'graphify "+version+"'; exit 0; fi\n"+
		"if [ \"$1\" = \"extract\" ]; then\n"+
		"  if [ \"${FAKE_GRAPHIFY_MODE:-}\" = fail ]; then echo fake failure >&2; exit 41; fi\n"+
		"  while [ \"$#\" -gt 0 ]; do if [ \"$1\" = \"--out\" ]; then out=$2; shift 2; else shift; fi; done\n"+
		"  if [ \"${FAKE_GRAPHIFY_MODE:-}\" = metadata-race ]; then rm -f \"$out/../current.json\" && ln -s \"$out\" \"$out/../current.json\"; fi\n"+
		"  if [ \"${FAKE_GRAPHIFY_MODE:-}\" = output-symlink ]; then ln -s \"$out\" \"$out/graphify-out\"; else mkdir -p \"$out/graphify-out\" || exit 1; fi\n"+
		"  printf '{\\\"nodes\\\":[]}' > \"$out/graphify-out/graph.json\"\n"+
		"  exit 0\nfi\n"+
		"printf 'delegated:%s\\n' \"$*\"\n")
	if err := os.Chmod(filepath.Join(f.bin, "graphify"), 0o755); err != nil {
		t.Fatal(err)
	}
}

func (f graphifyFixture) run(t *testing.T, args ...string) graphifyRun {
	t.Helper()
	return f.runEnv(t, nil, args...)
}

func (f graphifyFixture) runEnv(t *testing.T, extra []string, args ...string) graphifyRun {
	t.Helper()
	script, err := filepath.Abs("graphify")
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command("python3", append([]string{script}, args...)...)
	command.Dir = f.repo
	command.Env = append(os.Environ(), "XDG_CACHE_HOME="+f.cache, "PATH="+f.bin+":/usr/bin:/bin")
	command.Env = append(command.Env, extra...)
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	err = command.Run()
	return graphifyRun{stdout: stdout.String(), stderr: stderr.String(), err: err}
}

func mustRefresh(t *testing.T, fixture graphifyFixture) {
	t.Helper()
	if run := fixture.run(t, "refresh"); run.err != nil {
		t.Fatalf("refresh: %v\n%s", run.err, run.stderr)
	}
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = dir
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
	}
}

func graphifyExitCode(err error) int {
	if err == nil {
		return 0
	}
	if exit, ok := err.(*exec.ExitError); ok {
		return exit.ExitCode()
	}
	return -1
}

func readTree(t *testing.T, root string) []string {
	t.Helper()
	var paths []string
	if err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path != root {
			paths = append(paths, strings.TrimPrefix(path, root+string(os.PathSeparator)))
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return paths
}
