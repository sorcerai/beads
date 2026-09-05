package main

import (
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/steveyegge/beads/codemapops"
	"github.com/steveyegge/beads/internal/ui"
)

// Every text rendering the codemap verbs produce lives here, so the RunEs hold
// decisions and the shapes hold formatting.

func renderBuilt(w io.Writer, res codemapops.ApplyResult, langs []string, dropped int) {
	fmt.Fprintf(w, "%s Code map built: %d nodes, %d edges (%s)\n",
		ui.RenderPass("✓"), res.NodesUpserted, res.EdgesWritten, strings.Join(langs, ", "))
	if res.NodesDeleted > 0 {
		fmt.Fprintf(w, "  %s\n", ui.RenderMuted(fmt.Sprintf("%d nodes no longer in the tree were removed", res.NodesDeleted)))
	}
	renderDropped(w, dropped)
}

func renderDropped(w io.Writer, dropped int) {
	if dropped > 0 {
		fmt.Fprintf(w, "  %d unresolved references were dropped, not guessed\n", dropped)
	}
}

func renderRefreshed(w io.Writer, res refreshResult) {
	if len(res.Only) == 0 {
		fmt.Fprintf(w, "%s Code map already up to date\n", ui.RenderPass("✓"))
		return
	}
	fmt.Fprintf(w, "%s Code map refreshed: %d packages rescanned, %d nodes, %d edges\n",
		ui.RenderPass("✓"), len(res.Only), res.NodesUpserted, res.EdgesWritten)
	for _, p := range res.Only {
		fmt.Fprintf(w, "  %s\n", ui.RenderMuted(p))
	}
}

// renderCodemapStatus is the freshness report. The literal phrases "up to date" and
// "behind HEAD" are the answer a reader scans for, so they are fixed text.
func renderCodemapStatus(w io.Writer, st codemapStatus) {
	fmt.Fprintf(w, "%s %s\n", ui.RenderBold("Code map"), ui.RenderMuted(st.Shape.RepoID))
	if st.UpToDate {
		fmt.Fprintf(w, "  %s up to date at %s\n", ui.RenderPass("✓"), shortSHA(st.HeadSHA))
	} else if st.BehindKnown {
		fmt.Fprintf(w, "  %s behind HEAD by %d commit(s): indexed %s, HEAD %s\n",
			ui.RenderWarn("!"), st.Behind, shortSHA(st.Shape.HeadSHA), shortSHA(st.HeadSHA))
	} else {
		fmt.Fprintf(w, "  %s behind HEAD: indexed %s, HEAD %s\n",
			ui.RenderWarn("!"), shortSHA(st.Shape.HeadSHA), shortSHA(st.HeadSHA))
	}
	fmt.Fprintf(w, "  %d nodes, %d edges\n", st.Shape.Nodes, st.Shape.Edges)
	fmt.Fprintf(w, "  %d stale summaries\n", st.Shape.StaleSummaries)
	if len(st.Languages) > 0 {
		fmt.Fprintf(w, "  languages: %s\n", strings.Join(st.Languages, ", "))
	}
	if st.CacheKnown {
		fmt.Fprintf(w, "  cache written %s ago\n", roundAge(st.CacheAge))
	} else {
		fmt.Fprintf(w, "  %s\n", ui.RenderMuted("cache not written yet (bd codemap build writes it)"))
	}
	if !st.UpToDate {
		fmt.Fprintf(w, "  %s\n", ui.RenderMuted("run: bd codemap refresh"))
	}
}

func renderShape(w io.Writer, s codemapops.Shape, cacheAge time.Duration, dropped int) {
	fmt.Fprintf(w, "%s %s at %s\n", ui.RenderBold("Code map"), ui.RenderMuted(s.RepoID), shortSHA(s.HeadSHA))
	fmt.Fprintf(w, "  %d nodes, %d edges, %d stale summaries\n", s.Nodes, s.Edges, s.StaleSummaries)
	if cacheAge > 0 {
		fmt.Fprintf(w, "  cache written %s ago\n", roundAge(cacheAge))
	}
	for _, l := range s.Layers {
		fmt.Fprintf(w, "  %-16s %d packages, %d files\n", l.Layer, l.Packages, l.Files)
	}
	if len(s.TopFanIn) > 0 {
		fmt.Fprintf(w, "%s\n", ui.RenderBold("MOST IMPORTED"))
		for _, n := range s.TopFanIn {
			fmt.Fprintf(w, "  %s\n", nodeLine(n))
		}
	}
	renderDropped(w, dropped)
}

func renderFileContext(w io.Writer, fc codemapops.FileContext) {
	fmt.Fprintf(w, "%s %s\n", ui.RenderBold("FILE"), ui.RenderAccent(fc.Node.Path))
	if fc.Node.Summary != "" {
		fmt.Fprintf(w, "  %s\n", fc.Node.Summary)
	}
	if fc.Package.Path != "" {
		fmt.Fprintf(w, "  package %s\n", fc.Package.Path)
	}
	renderRefList(w, "imports", fc.Imports)
	renderRefList(w, "importers", fc.Importers)
	renderRefList(w, "tests", fc.Tests)
}

func renderPackageContext(w io.Writer, pc codemapops.PackageContext) {
	fmt.Fprintf(w, "%s %s\n", ui.RenderBold("PACKAGE"), ui.RenderAccent(pc.Node.Path))
	if pc.Node.Summary != "" {
		fmt.Fprintf(w, "  %s\n", pc.Node.Summary)
	}
	renderRefList(w, "files", pc.Files)
	renderRefList(w, "imports", pc.Imports)
	renderRefList(w, "importers", pc.Importers)
}

// renderIssueCodeContext is the CODE section `bd show` and `bd prime` append to
// an issue. quiet drops the per-file detail and keeps the one-line header.
func renderIssueCodeContext(w io.Writer, cc codemapops.IssueCodeContext, quiet bool) {
	if !cc.Indexed {
		if !quiet {
			fmt.Fprintf(w, "%s\n", ui.RenderMuted("CODE: repository not indexed (bd codemap build)"))
		}
		return
	}
	if len(cc.Files) == 0 {
		return
	}
	fmt.Fprintf(w, "%s %s\n", ui.RenderBold("CODE"), ui.RenderMuted(fmt.Sprintf("%d file(s)", len(cc.Files))))
	if quiet {
		return
	}
	for _, f := range cc.Files {
		fmt.Fprintf(w, "  %s %s\n", ui.RenderAccent(f.Path), ui.RenderMuted(fmt.Sprintf("(%s, %d touches)", f.Source, f.Touches)))
		if f.Context != nil && f.Context.Node.Summary != "" {
			fmt.Fprintf(w, "    %s\n", f.Context.Node.Summary)
		}
		for _, s := range f.Siblings {
			fmt.Fprintf(w, "    %s %s\n", ui.RenderMuted("also:"), ui.RenderID(s.ID))
		}
	}
}

// maxRefLines caps one list in the text rendering. A package-scoped list is
// routinely huge — `bd codemap show` on a file in cmd/bd reports 580 sibling
// test files — and a screen of paths is not the answer anyone came for. The
// COUNT is always exact; --json carries the whole list.
const maxRefLines = 20

func renderRefList(w io.Writer, label string, refs []codemapops.NodeRef) {
	if len(refs) == 0 {
		return
	}
	fmt.Fprintf(w, "  %s (%d)\n", ui.RenderBold(label), len(refs))
	shown := refs
	if len(shown) > maxRefLines {
		shown = shown[:maxRefLines]
	}
	for _, r := range shown {
		fmt.Fprintf(w, "    %s\n", nodeLine(r))
	}
	if rest := len(refs) - len(shown); rest > 0 {
		fmt.Fprintf(w, "    %s\n", ui.RenderMuted(fmt.Sprintf("... and %d more (--json for all)", rest)))
	}
}

func nodeLine(n codemapops.NodeRef) string {
	line := n.Path
	if n.Stale {
		line += " " + ui.RenderWarn("(stale summary)")
	}
	if n.IsTest {
		line += " " + ui.RenderMuted("(test)")
	}
	return line
}

func renderIssueFiles(w io.Writer, id string, files []codemapops.IssueFile) {
	if len(files) == 0 {
		fmt.Fprintf(w, "%s\n", ui.RenderMuted("No files linked to "+id))
		return
	}
	fmt.Fprintf(w, "%s %s\n", ui.RenderBold("FILES"), ui.RenderID(id))
	for _, f := range files {
		fmt.Fprintf(w, "  %-48s %-8s %3d touches  last seen %s\n",
			f.Path, f.Source, f.Touches, f.LastSeen.Format("2006-01-02"))
	}
}

func renderLinked(w io.Writer, id string, paths []string) {
	fmt.Fprintf(w, "%s Linked %d files to %s\n", ui.RenderPass("✓"), len(paths), ui.RenderID(id))
}

func renderIssueRefs(w io.Writer, path string, refs []codemapops.IssueRef) {
	if len(refs) == 0 {
		fmt.Fprintf(w, "%s\n", ui.RenderMuted("No issues touch "+path))
		return
	}
	fmt.Fprintf(w, "%s %s\n", ui.RenderBold("ISSUES"), ui.RenderAccent(path))
	for _, r := range refs {
		fmt.Fprintf(w, "  %s  %-10s %s\n", ui.RenderID(r.ID), r.Status, r.Title)
	}
}

// renderCodemapStale keeps the literal "N stale" phrasing the summarizer pass and the
// tests both read.
func renderCodemapStale(w io.Writer, refs []codemapops.NodeRef) {
	fmt.Fprintf(w, "%d stale summaries\n", len(refs))
	for _, r := range refs {
		fmt.Fprintf(w, "  %s\n", r.Path)
	}
}

// renderDepTree prints the BFS walk `deps` produced, already flattened to
// (depth, path) rows.
func renderDepTree(w io.Writer, root string, reverse bool, rows []depRow) {
	direction := "imports"
	if reverse {
		direction = "importers"
	}
	fmt.Fprintf(w, "%s %s %s\n", ui.RenderBold(strings.ToUpper(direction)), ui.RenderMuted("of"), ui.RenderAccent(root))
	if len(rows) == 0 {
		fmt.Fprintf(w, "  %s\n", ui.RenderMuted("none"))
		return
	}
	for _, r := range rows {
		fmt.Fprintf(w, "%s%s\n", strings.Repeat("  ", r.Depth), r.Path)
	}
}

func shortSHA(sha string) string {
	if len(sha) > 8 {
		return sha[:8]
	}
	if sha == "" {
		return "(none)"
	}
	return sha
}

func roundAge(d time.Duration) string {
	if d < time.Minute {
		return d.Round(time.Second).String()
	}
	return d.Round(time.Minute).String()
}
