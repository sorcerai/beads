package summarize

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseResponseValidatesEverything(t *testing.T) {
	batch := []Candidate{{Path: "a.go", BlobHash: strings.Repeat("1", 40)}, {Path: "b.go", BlobHash: strings.Repeat("2", 40)}}
	raw := "```json\n[{\"path\":\"a.go\",\"summary\":\"Does A\",\"layer\":\"cli\",\"tags\":[\"x\"]}," +
		"{\"path\":\"zzz.go\",\"summary\":\"not in batch\",\"layer\":\"cli\"}," +
		"{\"path\":\"b.go\",\"summary\":\"" + strings.Repeat("long ", 40) + "\",\"layer\":\"nope\"}]\n```"
	got, dropped := ParseResponse(raw, batch, []string{"cli", "storage"})
	if len(got) != 1 || got[0].Path != "a.go" || got[0].BlobHash != strings.Repeat("1", 40) || len(dropped) != 2 {
		t.Fatalf("got %+v dropped %v", got, dropped)
	}
}

func TestRunBatchesAndRetriesOnce(t *testing.T) {
	calls := 0
	call := func(prompt string) (string, error) {
		calls++
		if calls == 1 {
			return "", errors.New("transport")
		}
		return `[{"path":"a.go","summary":"A","layer":"cli"}]`, nil
	}
	out, rep, err := Run(context.Background(), call, []Candidate{{Path: "a.go", BlobHash: strings.Repeat("1", 40)}}, Options{Layers: []string{"cli"}, BatchSize: 25})
	if err != nil || len(out) != 1 || rep.Batches != 1 || calls != 2 {
		t.Fatalf("%v %+v calls=%d", err, rep, calls)
	}
}

// TestParseResponseDropsMalformedRatherThanPoisoningTheWrite: every check the
// store makes has to be made here, because the store rejects the WHOLE write on
// one bad item instead of refusing that row.
func TestParseResponseDropsMalformedRatherThanPoisoningTheWrite(t *testing.T) {
	batch := []Candidate{
		{Path: "ok.go", BlobHash: strings.Repeat("1", 40)},
		{Path: "blank.go", BlobHash: strings.Repeat("2", 40)},
		{Path: "tagged.go", BlobHash: strings.Repeat("3", 40)},
		{Path: "untracked.go"},
		{Path: "dup.go", BlobHash: strings.Repeat("4", 40)},
	}
	raw := `[
	  {"path":"ok.go","summary":"Fine","layer":"cli"},
	  {"path":"blank.go","summary":"   ","layer":"cli"},
	  {"path":"tagged.go","summary":"Too many","layer":"cli","tags":["a","b","c","d","e","f"]},
	  {"path":"untracked.go","summary":"No blob","layer":"cli"},
	  {"path":"dup.go","summary":"First","layer":"cli"},
	  {"path":"dup.go","summary":"Second","layer":"cli"}
	]`
	got, dropped := ParseResponse(raw, batch, []string{"cli"})
	if len(got) != 2 || got[0].Path != "ok.go" || got[1].Summary != "First" {
		t.Fatalf("kept %+v", got)
	}
	if len(dropped) != 4 {
		t.Fatalf("dropped %v", dropped)
	}
}

// A summary of 160 RUNES can exceed 160 bytes, which is what the store counts.
func TestParseResponseCountsBytesNotRunes(t *testing.T) {
	batch := []Candidate{{Path: "a.go", BlobHash: strings.Repeat("1", 40)}}
	long := strings.Repeat("é", 100) // 200 bytes, 100 runes
	got, dropped := ParseResponse(`[{"path":"a.go","summary":"`+long+`","layer":"cli"}]`, batch, []string{"cli"})
	if len(got) != 0 || len(dropped) != 1 {
		t.Fatalf("got %+v dropped %v", got, dropped)
	}
}

func TestParseResponseGarbageIsOneDrop(t *testing.T) {
	batch := []Candidate{{Path: "a.go", BlobHash: strings.Repeat("1", 40)}}
	if got, dropped := ParseResponse("I'm sorry, I can't do that.", batch, []string{"cli"}); len(got) != 0 || len(dropped) != 1 {
		t.Fatalf("got %+v dropped %v", got, dropped)
	}
}

func TestRunHonoursMaxFilesAndBatchesEvenly(t *testing.T) {
	var sizes []int
	call := func(prompt string) (string, error) {
		sizes = append(sizes, strings.Count(prompt, "--- FILE "))
		return "[]", nil
	}
	cands := make([]Candidate, 7)
	for i := range cands {
		cands[i] = Candidate{Path: string(rune('a'+i)) + ".go", BlobHash: strings.Repeat("1", 40)}
	}
	_, rep, err := Run(context.Background(), call, cands, Options{Layers: []string{"cli"}, BatchSize: 2, MaxFiles: 5})
	if err != nil || rep.Batches != 3 {
		t.Fatalf("%v %+v", err, rep)
	}
	if len(sizes) != 3 || sizes[0] != 2 || sizes[2] != 1 {
		t.Fatalf("batch sizes %v", sizes)
	}
}

// A batch that fails twice ends the run but keeps what earlier batches wrote.
func TestRunKeepsEarlierBatchesWhenOneFailsTwice(t *testing.T) {
	calls := 0
	call := func(prompt string) (string, error) {
		calls++
		if calls == 1 {
			return `[{"path":"a.go","summary":"A","layer":"cli"}]`, nil
		}
		return "", errors.New("transport")
	}
	cands := []Candidate{{Path: "a.go", BlobHash: strings.Repeat("1", 40)}, {Path: "b.go", BlobHash: strings.Repeat("2", 40)}}
	out, rep, err := Run(context.Background(), call, cands, Options{Layers: []string{"cli"}, BatchSize: 1})
	if err == nil || len(out) != 1 || rep.Batches != 2 || calls != 3 {
		t.Fatalf("%v out=%+v rep=%+v calls=%d", err, out, rep, calls)
	}
}

func TestRunStopsOnCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	called := false
	_, _, err := Run(ctx, func(string) (string, error) { called = true; return "[]", nil },
		[]Candidate{{Path: "a.go", BlobHash: strings.Repeat("1", 40)}}, Options{Layers: []string{"cli"}})
	if err == nil || called {
		t.Fatalf("err=%v called=%v", err, called)
	}
}

func TestRunStampsTheModelOnEveryItem(t *testing.T) {
	call := func(string) (string, error) { return `[{"path":"a.go","summary":"A","layer":"cli"}]`, nil }
	out, rep, err := Run(context.Background(), call, []Candidate{{Path: "a.go", BlobHash: strings.Repeat("1", 40)}},
		Options{Layers: []string{"cli"}, Model: "some-model"})
	if err != nil || len(out) != 1 || out[0].Model != "some-model" || rep.Written != 1 {
		t.Fatalf("%v %+v %+v", err, out, rep)
	}
}

func TestBuildPromptCarriesTheContract(t *testing.T) {
	p := BuildPrompt([]Candidate{{
		Path: "a/a.go", Package: "example.com/mini/a", Importers: 3,
		Exported: []string{"A", "B"}, Head: "package a\nfunc A() {}\n",
	}}, []string{"cli", "storage"})
	for _, want := range []string{"--- FILE a/a.go", "package: example.com/mini/a", "importers: 3", "exported: A, B", "cli, storage", "func A() {}"} {
		if !strings.Contains(p, want) {
			t.Fatalf("prompt missing %q:\n%s", want, p)
		}
	}
}

func TestHeadCapsLinesAndDropsBlanks(t *testing.T) {
	var src strings.Builder
	for i := 0; i < 100; i++ {
		src.WriteString("\n\nline\n")
	}
	got := Head(src.String())
	if n := strings.Count(got, "\n"); n != headLines {
		t.Fatalf("kept %d lines", n)
	}
	if strings.Contains(got, "\n\n") {
		t.Fatalf("blank lines survived: %q", got)
	}
}

func TestHeadCapsBytes(t *testing.T) {
	if got := Head(strings.Repeat(strings.Repeat("x", 500)+"\n", 20)); len(got) > headBytes {
		t.Fatalf("head is %d bytes", len(got))
	}
}

func TestExportedNamesGoAndRust(t *testing.T) {
	got := Exported("package a\n\nfunc A() {}\nfunc unexported() {}\ntype Thing struct{}\nfunc (t Thing) Method() {}\nconst Max = 1\npub fn rusty() {}\n")
	want := []string{"A", "Thing", "Method", "Max", "rusty"}
	if len(got) != len(want) {
		t.Fatalf("got %v want %v", got, want)
	}
	for _, w := range want {
		if !strings.Contains(strings.Join(got, ","), w) {
			t.Fatalf("got %v want %v", got, want)
		}
	}
}

func TestDefaultLayersIsRepoDirsPlusFixed(t *testing.T) {
	root := t.TempDir()
	for _, d := range []string{"cmd", "internal", "vendor", ".git", "testdata"} {
		mkdir(t, root, d)
	}
	got := DefaultLayers(root)
	joined := "," + strings.Join(got, ",") + ","
	for _, want := range []string{"cmd", "internal", "cli", "storage", "domain", "integration", "test", "tooling"} {
		if !strings.Contains(joined, ","+want+",") {
			t.Fatalf("layers %v missing %q", got, want)
		}
	}
	for _, unwanted := range []string{"vendor", ".git", "testdata"} {
		if strings.Contains(joined, ","+unwanted+",") {
			t.Fatalf("layers %v include %q", got, unwanted)
		}
	}
	for i := 1; i < len(got); i++ {
		if got[i-1] >= got[i] {
			t.Fatalf("layers not sorted/unique: %v", got)
		}
	}
}

func mkdir(t *testing.T, root, name string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(root, name), 0o755); err != nil {
		t.Fatal(err)
	}
}

// A batch the model answers with prose is 25 unsummarized files, not one drop.
func TestRunCountsEveryFileInALostBatch(t *testing.T) {
	calls := 0
	call := func(string) (string, error) {
		if calls++; calls == 1 {
			return `[{"path":"f0.go","summary":"A","layer":"cli"}]`, nil
		}
		return "Sorry, I can't help with that.", nil
	}
	cands := make([]Candidate, 26)
	for i := range cands {
		cands[i] = Candidate{Path: fmt.Sprintf("f%d.go", i), BlobHash: strings.Repeat("1", 40)}
	}
	out, rep, err := Run(context.Background(), call, cands, Options{Layers: []string{"cli"}, BatchSize: 25})
	if err != nil || len(out) != 1 || rep.Batches != 2 {
		t.Fatalf("%v out=%d rep=%+v", err, len(out), rep)
	}
	// 24 the first batch did not answer for, plus the whole lost second batch.
	if rep.Dropped != 25 {
		t.Fatalf("dropped %d: %v", rep.Dropped, rep.DroppedReasons)
	}
}

func TestParseResponseNormalizesLayerCase(t *testing.T) {
	batch := []Candidate{{Path: "a.go", BlobHash: strings.Repeat("1", 40)}}
	got, dropped := ParseResponse(`[{"path":"a.go","summary":"A","layer":" CLI "}]`, batch, []string{"cli"})
	if len(got) != 1 || got[0].Layer != "cli" || len(dropped) != 0 {
		t.Fatalf("got %+v dropped %v", got, dropped)
	}
}
