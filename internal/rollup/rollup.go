package rollup

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

const projectLabelPrefix = "project:"

// IssueSource is the narrow read interface rollup depends on. Satisfied by
// storage.DoltStorage. Two bulk reads only — keeps rollup free of raw SQL /
// *sql.DB and avoids N+1 over epics (spec C2/C3).
type IssueSource interface {
	SearchIssues(ctx context.Context, query string, filter types.IssueFilter) ([]*types.Issue, error)
	GetAllDependencyRecords(ctx context.Context) (map[string][]*types.Dependency, error)
}

// Options bounds the rollup (spec C2: pagination/caps are mandatory).
type Options struct {
	Project          string                          // optional: scope to one slug
	Limit            int                             // 0 => DefaultLimit
	CustomCategories map[string]types.StatusCategory // status name -> category
}

const DefaultLimit = 2000

type Card struct {
	ID       string    `json:"id"`
	Title    string    `json:"title"`
	Status   string    `json:"status"` // raw status name, always present
	Column   Column    `json:"column"`
	Priority int       `json:"priority"`
	Assignee string    `json:"assignee,omitempty"`
	Updated  time.Time `json:"updated_at"`
}

type Epic struct {
	Issue    Card   `json:"issue"`
	Column   Column `json:"column"`   // computed
	Conflict bool   `json:"conflict"` // closed epic w/ non-done child
	Children []Card `json:"children"`
}

type Project struct {
	Slug  string `json:"slug"`
	Epics []Epic `json:"epics"`
	Loose []Card `json:"loose"` // project-labeled, parentless non-epic edge cases
}

type Diagnostic struct {
	Kind    string `json:"kind"` // multi_project | invalid_graph
	IssueID string `json:"issue_id,omitempty"`
	Detail  string `json:"detail,omitempty"`
}

type Rollup struct {
	GeneratedAt time.Time    `json:"generated_at"`
	Projects    []Project    `json:"projects"`
	Diagnostics []Diagnostic `json:"diagnostics"`
}

func toCard(i *types.Issue, custom map[string]types.StatusCategory) Card {
	return Card{
		ID: i.ID, Title: i.Title, Status: string(i.Status),
		Column:   ColumnForStatus(i.Status, custom),
		Priority: i.Priority, Assignee: i.Assignee, Updated: i.UpdatedAt,
	}
}

// projectSlug returns the winning slug (first lexicographic project: label)
// and whether the issue had more than one (=> multi_project diagnostic).
func projectSlug(i *types.Issue) (slug string, multi bool) {
	var slugs []string
	for _, l := range i.Labels {
		if strings.HasPrefix(l, projectLabelPrefix) {
			slugs = append(slugs, strings.TrimPrefix(l, projectLabelPrefix))
		}
	}
	if len(slugs) == 0 {
		return "Unassigned", false
	}
	sort.Strings(slugs)
	return slugs[0], len(slugs) > 1
}

// buildParentMap derives child -> parent from parent-child dependency edges.
// Mirrors beads' canonical parent computation: first parent-child record wins
// (no sorting). allDeps is keyed by child issue ID; the parent is DependsOnID.
func buildParentMap(allDeps map[string][]*types.Dependency) map[string]string {
	parentOf := make(map[string]string, len(allDeps))
	for childID, deps := range allDeps {
		for _, d := range deps {
			if d.Type == types.DepParentChild {
				parentOf[childID] = d.DependsOnID
				break
			}
		}
	}
	return parentOf
}

func Compute(ctx context.Context, src IssueSource, opts Options) (*Rollup, error) {
	limit := opts.Limit
	if limit <= 0 {
		limit = DefaultLimit
	}
	filter := types.IssueFilter{Limit: limit}
	if opts.Project != "" {
		filter.Labels = []string{projectLabelPrefix + opts.Project}
	}
	issues, err := src.SearchIssues(ctx, "", filter)
	if err != nil {
		return nil, err
	}
	allDeps, err := src.GetAllDependencyRecords(ctx)
	if err != nil {
		return nil, err
	}
	parentOf := buildParentMap(allDeps)

	r := &Rollup{GeneratedAt: time.Now().UTC()}

	type pgroup struct {
		epicsByID map[string]*Epic
		order     []string
		loose     []Card
	}
	groups := map[string]*pgroup{}
	ensure := func(slug string) *pgroup {
		g := groups[slug]
		if g == nil {
			g = &pgroup{epicsByID: map[string]*Epic{}}
			groups[slug] = g
		}
		return g
	}
	ensure("Unassigned") // always emitted (spec)
	if opts.Project != "" {
		ensure(opts.Project)
	}

	// detectCycle walks the parent chain with a visited set; true on a cycle.
	detectCycle := func(startID string) bool {
		seen := map[string]bool{}
		cur := startID
		for {
			p, ok := parentOf[cur]
			if !ok {
				return false
			}
			if seen[cur] {
				return true
			}
			seen[cur] = true
			cur = p
		}
	}

	cycleReported := false
	for _, i := range issues {
		slug, multi := projectSlug(i)
		if multi {
			r.Diagnostics = append(r.Diagnostics, Diagnostic{Kind: "multi_project", IssueID: i.ID})
		}
		g := ensure(slug)
		if detectCycle(i.ID) {
			if !cycleReported {
				r.Diagnostics = append(r.Diagnostics, Diagnostic{Kind: "invalid_graph", Detail: "parent cycle or orphan loop"})
				cycleReported = true
			}
			continue // do not place cyclic nodes (avoids miscount/hang)
		}
		if _, hasParent := parentOf[i.ID]; !hasParent {
			// Parentless issue => epic row.
			card := toCard(i, opts.CustomCategories)
			e := g.epicsByID[i.ID]
			if e == nil {
				e = &Epic{Issue: card}
				g.epicsByID[i.ID] = e
				g.order = append(g.order, i.ID)
			} else {
				e.Issue = card
			}
		}
	}
	// Attach children to epics; collect loose.
	for _, i := range issues {
		parentID, hasParent := parentOf[i.ID]
		if !hasParent || detectCycle(i.ID) {
			continue
		}
		slug, _ := projectSlug(i)
		g := ensure(slug)
		card := toCard(i, opts.CustomCategories)
		if e := g.epicsByID[parentID]; e != nil {
			e.Children = append(e.Children, card)
		} else {
			g.loose = append(g.loose, card)
		}
	}

	// Compute epic column + conflict; assemble projects in stable slug order.
	var slugs []string
	for s := range groups {
		slugs = append(slugs, s)
	}
	sort.Strings(slugs)
	for _, s := range slugs {
		g := groups[s]
		p := Project{Slug: s, Loose: g.loose}
		for _, eid := range g.order {
			e := g.epicsByID[eid]
			allDone := true
			for _, c := range e.Children {
				if c.Column != ColumnDone {
					allDone = false
					break
				}
			}
			ownDone := e.Issue.Column == ColumnDone
			switch {
			case ownDone && allDone:
				e.Column = ColumnDone
			case ownDone && !allDone:
				e.Column = ColumnInProgress
				e.Conflict = true
			default:
				e.Column = e.Issue.Column
			}
			p.Epics = append(p.Epics, *e)
		}
		r.Projects = append(r.Projects, p)
	}
	return r, nil
}
