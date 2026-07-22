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
// The folder field reports which area folder holds the project (null at
// top level) — two spellings tried, since the dictionary varies.
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
    let folder = null;
    try { const f = p.folder(); folder = f ? f.name() : null; }
    catch (e) {
      try {
        const c = p.container();
        if (c && String(c.class()).toLowerCase().includes("folder")) folder = c.name();
      } catch (e2) {}
    }
    projects.push({id: p.id(), name: p.name(), status: status, defer: defer, folder: folder});
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
    // Dropped is a distinct state from completed in OmniFocus — a task
    // dropped years ago is NOT available and must not be listed as such.
    // effectivelyDropped also covers tasks inside dropped projects /
    // parents; older dictionaries may lack it, so fall back to plain
    // dropped, and degrade to including the task rather than failing.
    // ⚠ VERIFY-ON-MAC: confirm both spellings in Script Editor.
    let dropped = false;
    try { dropped = t.effectivelyDropped(); }
    catch (e) { try { dropped = t.dropped(); } catch (e2) {} }
    if (dropped) continue;
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

// jxaSetProjectStatus flips a project between active and on-hold — the
// two statuses deliberately supported. Done/dropped are omitted on
// purpose: completing or abandoning a whole project is a bigger decision
// than a task tick, and keeping the write surface small is a feature.
// Setting an AppleScript enum from JXA needs the right spelling, which
// varies ("on hold status" vs "on hold"); we try each and report the
// resulting status (normalised, same as list_projects) so the model sees
// what actually happened rather than what it asked for.
// ⚠ VERIFY-ON-MAC: confirm which spelling your OmniFocus accepts.
const jxaSetProjectStatus = `(() => {
  const of = Application("OmniFocus");
  const doc = of.defaultDocument;
  const id = %s;
  const status = %s; // "active" | "on-hold" — validated on the Go side
  const p = doc.flattenedProjects().find(x => x.id() === id);
  if (!p) { return JSON.stringify({ok: false, error: "no project with that id"}); }
  const spellings = status === "active"
    ? ["active status", "active"]
    : ["on hold status", "on hold"];
  let lastErr = null;
  for (const s of spellings) {
    try { p.status = s; lastErr = null; break; } catch (e) { lastErr = e; }
  }
  if (lastErr) { return JSON.stringify({ok: false, error: "could not set status: " + lastErr}); }
  const clean = s => String(s).replace(" status", "").replace(/\s+/g, "-");
  return JSON.stringify({ok: true, name: p.name(), status: clean(p.status())});
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

// ─────────────────────────────────────────────────────────────────────────────
// Folders (v0.7). Read, create, rename, and file projects into them.
// Deliberately absent: deleting or hiding folders — same philosophy as
// everywhere else: the write surface stays small and nothing is ever
// destroyed.
// ─────────────────────────────────────────────────────────────────────────────

const jxaListFolders = `(() => {
  const of = Application("OmniFocus");
  const doc = of.defaultDocument;
  const folders = [];
  for (const f of doc.flattenedFolders()) {
    let parent = null;
    try {
      const c = f.container();
      if (c && String(c.class()).toLowerCase().includes("folder")) parent = c.name();
    } catch (e) {}
    folders.push({id: f.id(), name: f.name(), parent: parent});
  }
  return JSON.stringify(folders);
})()`

// ⚠ VERIFY-ON-MAC: of.Folder(...) construction + push, and reading the
// id back after insertion.
const jxaCreateFolder = `(() => {
  const of = Application("OmniFocus");
  const doc = of.defaultDocument;
  const name = %s;
  const parentId = %s; // string or null
  let parent = null;
  if (parentId) {
    parent = doc.flattenedFolders().find(f => f.id() === parentId);
    if (!parent) { return JSON.stringify({ok: false, error: "no folder with that id"}); }
  }
  const made = of.Folder({name: name});
  (parent ? parent.folders : doc.folders).push(made);
  return JSON.stringify({ok: true, id: made.id(), name: made.name(), parent: parent ? parent.name() : null});
})()`

const jxaRenameFolder = `(() => {
  const of = Application("OmniFocus");
  const doc = of.defaultDocument;
  const id = %s;
  const newName = %s;
  const f = doc.flattenedFolders().find(x => x.id() === id);
  if (!f) { return JSON.stringify({ok: false, error: "no folder with that id"}); }
  const oldName = f.name();
  f.name = newName;
  return JSON.stringify({ok: true, id: f.id(), was: oldName, name: f.name()});
})()`

// ⚠ VERIFY-ON-MAC: the standard-suite move with an insertion location —
// of.move(p, {to: folder.projects.beginning}) — is the riskiest JXA
// spelling in this file. If it errors, Script Editor will say so and the
// error comes back in-band.
const jxaMoveProject = `(() => {
  const of = Application("OmniFocus");
  const doc = of.defaultDocument;
  const projectId = %s;
  const folderId = %s; // string, or null for top level
  const p = doc.flattenedProjects().find(x => x.id() === projectId);
  if (!p) { return JSON.stringify({ok: false, error: "no project with that id"}); }
  let folder = null;
  if (folderId) {
    folder = doc.flattenedFolders().find(f => f.id() === folderId);
    if (!folder) { return JSON.stringify({ok: false, error: "no folder with that id"}); }
  }
  try {
    of.move(p, {to: folder ? folder.projects.beginning : doc.projects.beginning});
  } catch (e) {
    return JSON.stringify({ok: false, error: "move failed: " + e});
  }
  return JSON.stringify({ok: true, name: p.name(), folder: folder ? folder.name() : null});
})()`
