// Package summarize turns file candidates into one-line summaries by asking a
// model, in batches, and dropping every answer it cannot verify. It holds no
// I/O of its own: the caller supplies the Caller, the heads and the blob
// hashes, which is what makes it testable without a model.
package summarize

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/steveyegge/beads/codemapops"
)

// DefaultBatchSize is how many files go in one prompt.
const DefaultBatchSize = 25

// maxSummaryBytes mirrors the store's own limit. The store REJECTS a whole
// write when one item exceeds it, so an over-long summary has to die here.
const maxSummaryBytes = 160

// maxTags mirrors the tags cap stated in the prompt.
const maxTags = 5

// Caller runs one prompt and returns the model's raw text.
type Caller func(prompt string) (string, error)

// Candidate is one file to summarize, with the context the prompt quotes.
type Candidate struct {
	Path, Package, Head, BlobHash string
	Exported                      []string
	Importers                     int
}

// Options configure a run.
type Options struct {
	Layers    []string
	BatchSize int
	MaxFiles  int
	Model     string
}

// Report says what a run did. Written counts the items that SURVIVED parsing,
// which is not what the store then accepted: it refuses a summary whose file
// moved under it, so the command reports its own written count. Dropped items are counted with their reasons and
// never retried: a malformed answer is the model's opinion, not a transport
// failure.
type Report struct {
	Batches, Written, Dropped int
	DroppedReasons            []string
}

// Run summarizes cands in batches. It retries a batch ONCE on a Caller error
// (transport), and returns whatever succeeded alongside the error if a batch
// fails twice, so a partial run still writes.
func Run(ctx context.Context, call Caller, cands []Candidate, opts Options) ([]codemapops.Summary, Report, error) {
	var (
		out []codemapops.Summary
		rep Report
	)
	if opts.BatchSize <= 0 {
		opts.BatchSize = DefaultBatchSize
	}
	if opts.MaxFiles > 0 && len(cands) > opts.MaxFiles {
		cands = cands[:opts.MaxFiles]
	}
	for start := 0; start < len(cands); start += opts.BatchSize {
		if err := ctx.Err(); err != nil {
			return out, rep, err
		}
		end := start + opts.BatchSize
		if end > len(cands) {
			end = len(cands)
		}
		batch := cands[start:end]
		rep.Batches++
		prompt := BuildPrompt(batch, opts.Layers)
		raw, err := call(prompt)
		if err != nil {
			raw, err = call(prompt) // one retry, transport only
		}
		if err != nil {
			return out, rep, fmt.Errorf("summarizing %s and %d more: %w", batch[0].Path, len(batch)-1, err)
		}
		items, dropped := ParseResponse(raw, batch, opts.Layers)
		for i := range items {
			items[i].Model = opts.Model
		}
		out = append(out, items...)
		rep.Dropped += len(dropped)
		rep.DroppedReasons = append(rep.DroppedReasons, dropped...)
	}
	rep.Written = len(out)
	return out, rep, nil
}

// modelItem is one entry as the model spells it.
type modelItem struct {
	Path    string   `json:"path"`
	Summary string   `json:"summary"`
	Layer   string   `json:"layer"`
	Tags    []string `json:"tags"`
}

// ParseResponse validates the model's answer against the batch it was asked
// about, returning the items worth writing and one reason per dropped item.
//
// The checks are a strict superset of the store's, which REJECTS a whole write
// (rather than refusing one row) when an item is malformed: one bad summary
// would otherwise lose the 24 good ones beside it. Length is in BYTES for the
// same reason — that is what the store measures.
func ParseResponse(raw string, batch []Candidate, layers []string) ([]codemapops.Summary, []string) {
	byPath := make(map[string]Candidate, len(batch))
	for _, c := range batch {
		byPath[c.Path] = c
	}
	// Case-folded lookup to the CANONICAL spelling: a model that answers
	// "CLI" meant the layer, and dropping the item over its shift key helps
	// nobody. The stored value is always the vocabulary's own spelling.
	allowed := make(map[string]string, len(layers))
	for _, l := range layers {
		allowed[strings.ToLower(l)] = l
	}

	var items []modelItem
	body := jsonArray(raw)
	if body == "" {
		return nil, lostBatch(batch, "no JSON array in the response")
	}
	if err := json.Unmarshal([]byte(body), &items); err != nil {
		return nil, lostBatch(batch, fmt.Sprintf("unparseable response: %v", err))
	}

	var (
		out     []codemapops.Summary
		dropped []string
	)
	// handled is every batch path the response said ANYTHING about, kept or
	// dropped. What is left over at the end is the silent case: files the model
	// simply omitted, which are as unsummarized as the ones it got wrong.
	handled := make(map[string]struct{}, len(items))
	seen := make(map[string]struct{}, len(items))
	for _, it := range items {
		path := strings.TrimSpace(it.Path)
		cand, ok := byPath[path]
		if !ok {
			dropped = append(dropped, fmt.Sprintf("%q: not in this batch", it.Path))
			continue
		}
		handled[path] = struct{}{}
		if _, dup := seen[path]; dup {
			dropped = append(dropped, path+": duplicate entry")
			continue
		}
		summary := strings.TrimSpace(it.Summary)
		if summary == "" {
			dropped = append(dropped, path+": empty summary")
			continue
		}
		if len(summary) > maxSummaryBytes {
			dropped = append(dropped, fmt.Sprintf("%s: summary is %d bytes (max %d)", path, len(summary), maxSummaryBytes))
			continue
		}
		layer, ok := allowed[strings.ToLower(strings.TrimSpace(it.Layer))]
		if !ok {
			dropped = append(dropped, fmt.Sprintf("%s: layer %q not in the vocabulary", path, it.Layer))
			continue
		}
		if len(it.Tags) > maxTags {
			dropped = append(dropped, fmt.Sprintf("%s: %d tags (max %d)", path, len(it.Tags), maxTags))
			continue
		}
		if len(cand.BlobHash) != 40 {
			dropped = append(dropped, path+": no blob hash (not tracked by git?)")
			continue
		}
		seen[path] = struct{}{}
		out = append(out, codemapops.Summary{
			Path: path, Summary: summary, Layer: layer,
			Tags: it.Tags, BlobHash: cand.BlobHash,
		})
	}
	for _, c := range batch {
		if _, ok := handled[c.Path]; !ok {
			dropped = append(dropped, c.Path+": no entry in the response")
		}
	}
	return out, dropped
}

// lostBatch reports one drop PER FILE when a whole batch is unusable. A single
// reason would let "25 written, 1 dropped" hide 25 unsummarized files, which is
// the one number the operator reads to decide whether to run it again.
func lostBatch(batch []Candidate, reason string) []string {
	out := make([]string, 0, len(batch))
	for _, c := range batch {
		out = append(out, c.Path+": "+reason)
	}
	return out
}

// jsonArray finds the JSON array in a response that may be fenced, prefaced, or
// both. Empty when there is none.
func jsonArray(raw string) string {
	s := strings.TrimSpace(raw)
	if i := strings.Index(s, "```"); i >= 0 {
		s = s[i+3:]
		if nl := strings.IndexByte(s, '\n'); nl >= 0 {
			s = s[nl+1:] // drop the ```json language tag
		}
		if j := strings.Index(s, "```"); j >= 0 {
			s = s[:j]
		}
	}
	start, end := strings.Index(s, "["), strings.LastIndex(s, "]")
	if start < 0 || end < start {
		return ""
	}
	return s[start : end+1]
}
