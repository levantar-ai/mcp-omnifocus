package main

// ─────────────────────────────────────────────────────────────────────────────
// LESSON 1 — The bridge pattern.
//
// An MCP server is two things glued together:
//   (a) the MCP plumbing  — declaring tools, speaking JSON-RPC (main.go)
//   (b) the actual work   — here, talking to OmniFocus
//
// We put (b) behind a tiny interface. Why bother, for four functions?
// Because the real implementation shells out to `osascript`, which only
// exists on macOS with OmniFocus installed — but we want to unit-test the
// tool handlers anywhere (they were in fact first tested on a Linux box).
// Any seam like this makes an MCP server dramatically easier to develop.
// ─────────────────────────────────────────────────────────────────────────────

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// Bridge runs a JavaScript-for-Automation (JXA) script and returns its
// stdout. JXA is macOS's JavaScript flavour of AppleScript — we use it
// instead of classic AppleScript because it can JSON.stringify its results,
// which saves us from parsing AppleScript's comma-soup output format.
type Bridge interface {
	RunJXA(ctx context.Context, script string) (string, error)
}

// OsascriptBridge is the real implementation: it invokes the macOS
// `osascript` binary with `-l JavaScript`. Each call is a fresh process —
// slow-ish (~200ms) but stateless and robust, which is the right trade
// for a personal task manager.
type OsascriptBridge struct{}

func (OsascriptBridge) RunJXA(ctx context.Context, script string) (string, error) {
	cmd := exec.CommandContext(ctx, "osascript", "-l", "JavaScript", "-e", script)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		// Surface osascript's stderr — it's where JXA errors (and macOS
		// automation-permission refusals) appear. Losing this makes the
		// server undebuggable.
		return "", fmt.Errorf("osascript failed: %w — stderr: %s", err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(stdout.String()), nil
}

// ─────────────────────────────────────────────────────────────────────────────
// LESSON 2 — The JXA scripts.
//
// Each script is an IIFE returning JSON.stringify(...) — osascript prints
// the expression's value, so our Go side always receives a JSON string.
//
// ⚠ VERIFY-ON-MAC: JXA's OmniFocus dictionary has quirks. Before trusting
// each script, paste it into Script Editor (language: JavaScript) on your
// Mac and run it. The two marked candidates for adjustment are
// parseTasksInto and markComplete — their exact JXA spellings vary by
// OmniFocus version. This is deliberately part of the exercise.
// ─────────────────────────────────────────────────────────────────────────────

// VERIFIED-ON-MAC (2026-07): returns real project JSON.
// Field note from that verification: JXA renders status enums verbosely
// ("on hold status") — we normalise to "active" / "on-hold" / "done" /
// "dropped" here, at the boundary, so the model never sees the quirk.
// %s is includeAll (boolean): by default only active projects return —
// a real database is dominated by done/on-hold entries the model rarely
// wants, and smaller tool results mean better model behaviour.
const jxaListProjects = `(() => {
  const of = Application("OmniFocus");
  const doc = of.defaultDocument;
  const includeAll = %s;
  const clean = s => String(s).replace(" status", "").replace(/\s+/g, "-");
  const projects = [];
  for (const p of doc.flattenedProjects()) {
    const status = clean(p.status());
    if (!includeAll && status !== "active") continue;
    // defer matters: OmniFocus keeps future-deferred projects in "active"
    // status — the defer date is the only signal they're not live yet.
    // Wrapped in try/catch defensively; if this property misbehaves on
    // some OmniFocus version, we degrade to defer:null rather than fail.
    let defer = null;
    try { const d = p.deferDate(); defer = d ? d.toISOString() : null; } catch (e) {}
    projects.push({id: p.id(), name: p.name(), status: status, defer: defer});
  }
  return JSON.stringify(projects);
})()`

// jxaListTasks filters in plain JavaScript rather than using JXA's
// `whose` clauses — `whose` is faster but famously fragile. For a
// personal database (hundreds of tasks) the simple way is fine.
// %s placeholders are filled by Go with JSON-encoded values, which is
// our injection-safety mechanism: the filter arrives as data, not code.
const jxaListTasks = `(() => {
  const of = Application("OmniFocus");
  const doc = of.defaultDocument;
  const wantProject = %s;   // string or null
  const flaggedOnly = %s;   // boolean
  const tasks = [];
  for (const t of doc.flattenedTasks()) {
    if (t.completed()) continue;
    if (flaggedOnly && !t.flagged()) continue;
    const proj = t.containingProject();
    const projName = proj ? proj.name() : null;
    if (wantProject && projName !== wantProject) continue;
    const due = t.dueDate();
    const row = {
      id: t.id(),
      name: t.name(),
      project: projName,
      flagged: t.flagged(),
      due: due ? due.toISOString() : null,
      defer: null, planned: null, estimateMin: null,
    };
    try { const d = t.deferDate(); row.defer = d ? d.toISOString() : null; } catch (e) {}
    try { const p = t.plannedDate(); row.planned = p ? p.toISOString() : null; } catch (e) {}
    try { const m = t.estimatedMinutes(); row.estimateMin = m || null; } catch (e) {}
    tasks.push(row);
  }
  return JSON.stringify(tasks);
})()`

// jxaAddTasks v2. Field evidence killed the transport-text approach: it
// parsed none of our annotations (projects, @defer, tags all landed as
// literal text in task names). Now Go parses the TaskPaper (taskpaper.go)
// and this script receives structured groups, creating task OBJECTS with
// real properties — deferDate, dueDate, flagged — filed into named
// projects or the inbox. Unknown projects are reported back in-band so
// the model can list projects and retry rather than silently misfiling.
// ⚠ VERIFY-ON-MAC: of.Task(...)/of.InboxTask(...) construction + push.
const jxaAddTasks = `(() => {
  const of = Application("OmniFocus");
  const doc = of.defaultDocument;
  const groups = %s; // [{project: "" | name, tasks: [{name, note?, flagged?, defer?, due?}]}]
  const out = {created: 0, projectsNotFound: [], warnings: []};
  for (const g of groups) {
    let target = null;
    if (g.project) {
      target = doc.flattenedProjects().find(p => p.name() === g.project);
      if (!target) { out.projectsNotFound.push(g.project); continue; }
    }
    for (const t of g.tasks) {
      const props = {name: t.name};
      if (t.note) props.note = t.note;
      if (t.flagged) props.flagged = true;
      if (t.defer) props.deferDate = new Date(t.defer);
      if (t.due) props.dueDate = new Date(t.due);
      let made;
      if (target) {
        made = of.Task(props); target.tasks.push(made);
      } else {
        made = of.InboxTask(props); doc.inboxTasks.push(made);
      }
      // Newer/optional properties set AFTER creation, each in its own
      // try/catch: if a property doesn't exist in this OmniFocus version
      // the task still lands and we report a warning instead of failing.
      // ⚠ VERIFY-ON-MAC: plannedDate is an OmniFocus 4 addition — the JXA
      // spelling is the thing to confirm (see README probe snippet).
      if (t.planned) {
        try { made.plannedDate = new Date(t.planned); }
        catch (e) { out.warnings.push("plannedDate not settable: " + e); }
      }
      if (t.estimateMin) {
        try { made.estimatedMinutes = t.estimateMin; }
        catch (e) { out.warnings.push("estimatedMinutes not settable: " + e); }
      }
      out.created++;
    }
  }
  return JSON.stringify(out);
})()`

// jxaUpdateTask applies a partial patch to one task by id. Only keys
// present in the patch are touched; a null value clears that field.
// Each setter runs in its own try/catch so one unsupported property
// (on an older OmniFocus) degrades to a warning, not a failed update.
// plannedDate/estimatedMinutes spellings: VERIFIED-ON-MAC 2026-07 via
// v0.4's add path, which exercises the same properties.
const jxaUpdateTask = `(() => {
  const of = Application("OmniFocus");
  const doc = of.defaultDocument;
  const id = %s;
  const patch = %s;
  const t = doc.flattenedTasks().find(x => x.id() === id);
  if (!t) { return JSON.stringify({ok: false, error: "no task with that id"}); }
  const out = {ok: true, applied: [], warnings: []};
  const setters = {
    name:        v => { t.name = v; },
    note:        v => { t.note = v; },
    flagged:     v => { t.flagged = v; },
    defer:       v => { t.deferDate = (v === null ? null : new Date(v)); },
    due:         v => { t.dueDate = (v === null ? null : new Date(v)); },
    planned:     v => { t.plannedDate = (v === null ? null : new Date(v)); },
    estimateMin: v => { t.estimatedMinutes = v; },
  };
  for (const k of Object.keys(patch)) {
    if (!setters[k]) { out.warnings.push("unknown field: " + k); continue; }
    try { setters[k](patch[k]); out.applied.push(k); }
    catch (e) { out.warnings.push(k + ": " + e); }
  }
  out.name = t.name();
  return JSON.stringify(out);
})()`

// VERIFIED-ON-MAC (2026-07): `markComplete` spelling works as-is.
const jxaCompleteTask = `(() => {
  const of = Application("OmniFocus");
  const doc = of.defaultDocument;
  const id = %s;
  const task = doc.flattenedTasks().find(t => t.id() === id);
  if (!task) { return JSON.stringify({ok: false, error: "no task with that id"}); }
  of.markComplete(task);
  return JSON.stringify({ok: true, name: task.name()});
})()`
