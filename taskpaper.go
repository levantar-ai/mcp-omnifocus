package main

// ─────────────────────────────────────────────────────────────────────────────
// LESSON 6 — Parse at the boundary you control.
//
// v0.2 handed TaskPaper text to OmniFocus's "transport text" parser and
// hoped. Field evidence (five inbox tasks with "@defer(...)" embedded in
// their NAMES) proved transport text parses none of our annotations. The
// fix is a principle, not a patch: parse the rich format HERE, in Go,
// where we can test it — and hand the JXA side dumb, structured data to
// turn into task objects with real properties.
// ─────────────────────────────────────────────────────────────────────────────

import (
	"regexp"
	"strings"
)

type TPTask struct {
	Name        string `json:"name"`
	Note        string `json:"note,omitempty"`
	Flagged     bool   `json:"flagged,omitempty"`
	Defer       string `json:"defer,omitempty"` // ISO-ish local datetime, e.g. 2026-07-23T09:00
	Due         string `json:"due,omitempty"`
	Planned     string `json:"planned,omitempty"`     // when Andy intends to DO it (OF4 planned date)
	EstimateMin int    `json:"estimateMin,omitempty"` // duration in minutes
}

// TPGroup is a batch of tasks bound for one destination: a named project,
// or the inbox when Project is empty.
type TPGroup struct {
	Project string   `json:"project"`
	Tasks   []TPTask `json:"tasks"`
}

// Annotations we consume or deliberately discard. @parallel/@autodone are
// PROJECT settings in Andy's TaskPaper exports — meaningless on a task, and
// as transport tags they'd become junk literal tags. Anything not listed
// here stays in the name untouched (emails like a@b.org must survive).
var (
	reFlagged  = regexp.MustCompile(`\s*@flagged\b`)
	reDefer    = regexp.MustCompile(`\s*@defer\(([^)]*)\)`)
	reDue      = regexp.MustCompile(`\s*@due\(([^)]*)\)`)
	rePlanned  = regexp.MustCompile(`\s*@plan(?:ned)?\(([^)]*)\)`) // @planned(...) or @plan(...)
	reEstimate = regexp.MustCompile(`\s*@(?:estimate|duration)\(([^)]*)\)`)
	reDiscard  = regexp.MustCompile(`\s*@(parallel|autodone|context|tags)\([^)]*\)`)
	reDashLine = regexp.MustCompile(`^(\s*)-\s+(.*)$`)
	reEstPart  = regexp.MustCompile(`(?i)(\d+)\s*(h|m|hr|hrs|min|mins)?`)
)

// normDate turns "2026-07-23 09:00" into "2026-07-23T09:00" so JXA's
// `new Date(...)` parses it reliably; already-ISO strings pass through.
func normDate(s string) string {
	return strings.Replace(strings.TrimSpace(s), " ", "T", 1)
}

// parseEstimate accepts "30m", "2h", "1h30m", "90" (bare = minutes) and
// returns whole minutes. Anything unparseable returns 0 (annotation is
// simply dropped) — a wrong duration is worse than none.
func parseEstimate(s string) int {
	total := 0
	for _, m := range reEstPart.FindAllStringSubmatch(strings.TrimSpace(s), -1) {
		n := 0
		for _, c := range m[1] {
			n = n*10 + int(c-'0')
		}
		unit := strings.ToLower(m[2])
		if unit == "h" || unit == "hr" || unit == "hrs" {
			total += n * 60
		} else {
			total += n
		}
	}
	return total
}

// ParseTaskPaper converts a TaskPaper block into destination groups.
//
// Structure rules, matching Andy's conventions:
//   - A dash-line whose next dash-line is deeper is a CONTAINER (folder or
//     project). The innermost container above a task names its project.
//     Outer containers (area folders) are ignored — folders can't be
//     created through this path.
//   - Classic "Name:" project lines (no dash) also open a container.
//   - Deeper non-dash lines under a task are its note.
//   - defaultProject applies to tasks with no container of their own.
func ParseTaskPaper(text, defaultProject string) []TPGroup {
	type line struct {
		indent int
		isDash bool
		text   string
	}
	var lines []line
	for _, raw := range strings.Split(text, "\n") {
		if strings.TrimSpace(raw) == "" {
			continue
		}
		if m := reDashLine.FindStringSubmatch(raw); m != nil {
			lines = append(lines, line{indent: len(m[1]), isDash: true, text: m[2]})
		} else {
			trimmed := strings.TrimRight(raw, " \t")
			indent := len(trimmed) - len(strings.TrimLeft(trimmed, " \t"))
			lines = append(lines, line{indent: indent, isDash: false, text: strings.TrimSpace(trimmed)})
		}
	}

	groups := map[string]*TPGroup{}
	order := []string{}
	addTask := func(project string, t TPTask) {
		g, ok := groups[project]
		if !ok {
			g = &TPGroup{Project: project}
			groups[project] = g
			order = append(order, project)
		}
		g.Tasks = append(g.Tasks, t)
	}

	// containerAt tracks the container name per indent depth.
	containers := map[int]string{}
	containerFor := func(indent int) string {
		best, bestIndent := defaultProject, -1
		for ci, name := range containers {
			if ci < indent && ci > bestIndent {
				best, bestIndent = name, ci
			}
		}
		return best
	}
	clearDeeper := func(indent int) {
		for ci := range containers {
			if ci >= indent {
				delete(containers, ci)
			}
		}
	}

	var lastTask *TPTask
	var lastTaskProject string

	for i := 0; i < len(lines); i++ {
		ln := lines[i]

		// Plain "Name:" project line.
		if !ln.isDash && strings.HasSuffix(ln.text, ":") {
			clearDeeper(ln.indent)
			containers[ln.indent] = cleanName(strings.TrimSuffix(ln.text, ":"))
			lastTask = nil
			continue
		}

		// Note line: attaches to the task above it.
		if !ln.isDash {
			if lastTask != nil {
				if lastTask.Note != "" {
					lastTask.Note += "\n"
				}
				lastTask.Note += ln.text
			}
			continue
		}

		// Dash line: container if the next dash-line is deeper.
		isContainer := false
		for j := i + 1; j < len(lines); j++ {
			if lines[j].isDash {
				isContainer = lines[j].indent > ln.indent
				break
			}
		}
		if isContainer {
			clearDeeper(ln.indent)
			containers[ln.indent] = cleanName(ln.text)
			lastTask = nil
			continue
		}

		// Task line: extract annotations, then file it.
		t := TPTask{}
		name := ln.text
		if reFlagged.MatchString(name) {
			t.Flagged = true
			name = reFlagged.ReplaceAllString(name, "")
		}
		if m := reDefer.FindStringSubmatch(name); m != nil {
			t.Defer = normDate(m[1])
			name = reDefer.ReplaceAllString(name, "")
		}
		if m := reDue.FindStringSubmatch(name); m != nil {
			t.Due = normDate(m[1])
			name = reDue.ReplaceAllString(name, "")
		}
		if m := rePlanned.FindStringSubmatch(name); m != nil {
			t.Planned = normDate(m[1])
			name = rePlanned.ReplaceAllString(name, "")
		}
		if m := reEstimate.FindStringSubmatch(name); m != nil {
			t.EstimateMin = parseEstimate(m[1])
			name = reEstimate.ReplaceAllString(name, "")
		}
		name = reDiscard.ReplaceAllString(name, "")
		t.Name = strings.TrimSpace(name)
		if t.Name == "" {
			continue
		}
		project := containerFor(ln.indent)
		clearDeeper(ln.indent + 1)
		addTask(project, t)
		g := groups[project]
		lastTask = &g.Tasks[len(g.Tasks)-1]
		lastTaskProject = project
		_ = lastTaskProject
	}

	out := make([]TPGroup, 0, len(order))
	for _, name := range order {
		out = append(out, *groups[name])
	}
	return out
}

// cleanName strips convention annotations from container names —
// "Project X: @parallel(true)" and friends.
func cleanName(s string) string {
	s = reDiscard.ReplaceAllString(s, "")
	s = reFlagged.ReplaceAllString(s, "")
	s = reDefer.ReplaceAllString(s, "")
	s = reDue.ReplaceAllString(s, "")
	s = rePlanned.ReplaceAllString(s, "")
	s = reEstimate.ReplaceAllString(s, "")
	return strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(s), ":"))
}
