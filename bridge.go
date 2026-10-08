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

// jxaListTasks reads each property for ALL tasks in one Apple event
// (tasks.name(), tasks.id(), ...) and filters in plain JavaScript,
// instead of looping over every task and asking ~10 questions per task.
// Field note (2026-10): with ~1200 tasks (700 of them completed) the
// per-task loop took ~41 s for list_tasks; this takes ~1 s. What the
// measuring taught us (all on the same database):
//   - Bulk-reading a property over the whole flattenedTasks collection
//     costs 20-150 ms per property (~0.5 s for all of them).
//   - A `whose` clause looks like the obvious optimisation but is NOT:
//     the specifier is re-evaluated on every property read (~5 s each),
//     so ten reads on 500 matches cost ~50 s. Do not filter with `whose`
//     and then bulk-read; bulk-read first, filter after.
//   - Bulk-reading tags per task (tasks.tags.name()) is catastrophically
//     slow (>2 min). Instead ask each TAG for its task ids (a handful of
//     tags) and join in JS: ~0.7 s.
//   - Every optional property is read inside its own try, so a missing
//     spelling degrades to null instead of failing the whole call.
//
// Behaviour change vs. the per-task loop: a project's ROOT task (the
// project itself, which doc.flattenedTasks also returns, id == project id)
// is no longer listed as if it were a task. Projects belong to
// list_projects.
//
// %s placeholders are filled by Go with JSON-encoded values, which is
// our injection-safety mechanism: the filter arrives as data, not code.
const jxaListTasks = `(() => {
  const of = Application("OmniFocus");
  const doc = of.defaultDocument;
  const wantProject = %s;   // string or null
  const flaggedOnly = %s;   // boolean
  const read = (get) => { try { return get(); } catch (e) { return null; } };
  const T = doc.flattenedTasks;
  const completed = T.completed();
  const flagged = T.flagged();
  const ids = T.id(), names = T.name();
  const projs = read(() => T.containingProject.name());
  const due = read(() => T.dueDate());
  const defer = read(() => T.deferDate());
  const planned = read(() => T.plannedDate());
  const est = read(() => T.estimatedMinutes());
  // Dropped is a distinct state from completed in OmniFocus — a task
  // dropped years ago is NOT available and must not be listed as such.
  // effectivelyDropped also covers tasks inside dropped projects /
  // parents; older dictionaries may lack it, so fall back to plain
  // dropped, and degrade to including the task rather than failing.
  const dropped = read(() => T.effectivelyDropped()) || read(() => T.dropped());
  const rootIds = new Set(doc.flattenedProjects.id());
  const iso = (arr, i) => (arr && arr[i] ? arr[i].toISOString() : null);
  const rows = [];
  for (let i = 0; i < ids.length; i++) {
    if (completed[i]) continue;
    if (flaggedOnly && !flagged[i]) continue;
    if (dropped && dropped[i]) continue;
    if (rootIds.has(ids[i])) continue;
    const projName = projs && projs[i] ? projs[i] : null;
    if (wantProject && projName !== wantProject) continue;
    rows.push({
      id: ids[i],
      name: names[i],
      project: projName,
      flagged: flagged[i],
      due: iso(due, i),
      defer: iso(defer, i),
      planned: iso(planned, i),
      estimateMin: (est && est[i]) || null,
      tags: [],
    });
  }
  if (rows.length > 0) {
    const byId = {};
    for (const r of rows) byId[r.id] = r;
    // Tags, read-only (v0.8): "andy rea" on a task marks it as handed to
    // Andy's co-founder in the collaboration bridge. Join from the tag
    // side (see header note); degrades to [] if the spelling differs.
    try {
      for (const tg of doc.flattenedTags()) {
        const tname = tg.name();
        let tids = [];
        try { tids = tg.tasks.id(); } catch (e) {}
        for (const id of tids) if (byId[id]) byId[id].tags.push(tname);
      }
    } catch (e) {}
  }
  return JSON.stringify(rows);
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
    // Tag writes (v0.9), added when board-sync assignment went two-way.
    // ⚠ VERIFY-ON-MAC: of.add(tag, {to: t.tags}) / of.remove(...) — the
    // standard-suite add/remove with an insertion container, same family
    // as the of.move spelling that verified fine for move_project.
    addTags: v => {
      for (const name of v) {
        let tag = doc.flattenedTags().find(x => x.name() === name);
        if (!tag) { tag = of.Tag({name: name}); doc.tags.push(tag); }
        of.add(tag, {to: t.tags});
      }
    },
    removeTags: v => {
      for (const name of v) {
        const tag = doc.flattenedTags().find(x => x.name() === name);
        if (tag) { of.remove(tag, {from: t.tags}); }
      }
    },
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

// ─────────────────────────────────────────────────────────────────────────────
// Container operations via Omni Automation (v0.11).
//
// Field history worth keeping: the AppleScript-dictionary route
// (of.move, folder.projects.push) failed with nil-container errors on
// EVERY spelling — objects fetched through flattened* lookups don't
// resolve as insertion containers. The fix was not another spelling but
// another DIALECT: OmniFocus embeds its own official automation
// JavaScript ("Omni Automation"), reachable from JXA via
// evaluateJavascript, with purpose-built moveTasks/moveSections/
// positional constructors. When an app offers a first-party automation
// API, use it — the AppleScript bridge is the emulation layer.
//
// omniInner runs an Omni Automation script inside OmniFocus and returns
// its result (the inner script must return a JSON string).
// ⚠ VERIFY-ON-MAC: evaluateJavascript spelling, Task/Project/
// Folder.byIdentifier, moveTasks, moveSections, positional constructors.
// ─────────────────────────────────────────────────────────────────────────────

// omniOuter wraps an inner Omni Automation script (passed JSON-encoded)
// for execution via osascript.
const omniOuter = `(() => {
  const of = Application("OmniFocus");
  return of.evaluateJavascript(%s);
})()`

// omniResolveProject is the OA-side helper for the id dialect gap:
// AppleScript reports a project's id as its ROOT TASK's id, while
// Project.byIdentifier wants the project's own key. Resolve by every
// route so ids from list_projects always work.
const omniResolveProject = `
  const resolveProject = (id) => {
    let p = null;
    try { p = Project.byIdentifier(id); } catch (e) {}
    if (!p) { try { const t = Task.byIdentifier(id); if (t && t.containingProject) { p = t.containingProject; } } catch (e) {} }
    if (!p) { try { p = flattenedProjects.find(pr => pr.task && pr.task.id.primaryKey === id) || null; } catch (e) {} }
    return p;
  };
`

// scriptCreateProject builds the full osascript for creating a project,
// positionally inside a folder when folderId is given.
func scriptCreateProject(name string, folderID string) string {
	inner := fmt.Sprintf(`(() => {
  const name = %s;
  const folderId = %s;
  let pos = library.ending;
  if (folderId) {
    const f = Folder.byIdentifier(folderId);
    if (!f) { return JSON.stringify({ok: false, error: "no folder with that id"}); }
    pos = f;
  }
  const p = new Project(name, pos);
  // Report the ROOT TASK's key as the id — that's the AppleScript-flavour
  // id list_projects returns, keeping the id-join consistent everywhere.
  const outId = (p.task && p.task.id) ? p.task.id.primaryKey : p.id.primaryKey;
  return JSON.stringify({ok: true, id: outId, name: p.name, folder: folderId ? p.parentFolder.name : null});
})()`, mustJSON(name), mustJSON(orNull(folderID)))
	return fmt.Sprintf(omniOuter, mustJSON(inner))
}

// scriptCreateFolder builds the osascript for creating a folder,
// optionally inside a parent folder.
func scriptCreateFolder(name string, parentID string) string {
	inner := fmt.Sprintf(`(() => {
  const name = %s;
  const parentId = %s;
  let pos = library.ending;
  if (parentId) {
    const f = Folder.byIdentifier(parentId);
    if (!f) { return JSON.stringify({ok: false, error: "no folder with that id"}); }
    pos = f;
  }
  const made = new Folder(name, pos);
  return JSON.stringify({ok: true, id: made.id.primaryKey, name: made.name, parent: parentId ? made.parent.name : null});
})()`, mustJSON(name), mustJSON(orNull(parentID)))
	return fmt.Sprintf(omniOuter, mustJSON(inner))
}

// scriptMoveTask builds the osascript for filing a task into a project
// via Omni Automation's moveTasks.
func scriptMoveTask(taskID, projectID string) string {
	inner := fmt.Sprintf(`(() => {`+omniResolveProject+`
  const t = Task.byIdentifier(%s);
  if (!t) { return JSON.stringify({ok: false, error: "no task with that id"}); }
  const p = resolveProject(%s);
  if (!p) { return JSON.stringify({ok: false, error: "no project with that id (tried direct, root-task and scan resolution)"}); }
  moveTasks([t], p);
  return JSON.stringify({ok: true, name: t.name, project: p.name});
})()`, mustJSON(taskID), mustJSON(projectID))
	return fmt.Sprintf(omniOuter, mustJSON(inner))
}

// scriptMoveProject builds the osascript for filing a project into a
// folder (or the top level) via Omni Automation's moveSections.
func scriptMoveProject(projectID, folderID string) string {
	inner := fmt.Sprintf(`(() => {`+omniResolveProject+`
  const p = resolveProject(%s);
  if (!p) { return JSON.stringify({ok: false, error: "no project with that id (tried direct, root-task and scan resolution)"}); }
  const folderId = %s;
  let pos = library.ending;
  let folderName = null;
  if (folderId) {
    const f = Folder.byIdentifier(folderId);
    if (!f) { return JSON.stringify({ok: false, error: "no folder with that id"}); }
    pos = f; folderName = f.name;
  }
  moveSections([p], pos);
  return JSON.stringify({ok: true, name: p.name, folder: folderName});
})()`, mustJSON(projectID), mustJSON(orNull(folderID)))
	return fmt.Sprintf(omniOuter, mustJSON(inner))
}

// orNull maps Go's empty string to JSON null for optional ids.
func orNull(s string) any {
	if s == "" {
		return nil
	}
	return s
}

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

// VERIFIED-ON-MAC (2026-07, the hard way): asking each folder for its
// container returned null parents across the board — the class-check
// spelling doesn't hold on this dictionary. So don't ask; WALK. Starting
// from doc.folders() (top level only) and recursing into each folder's
// own folders(), the parent is known by construction, and we can emit a
// full breadcrumb path as a bonus. When two ways exist to learn a fact,
// prefer the one where the answer can't be wrong.
const jxaListFolders = `(() => {
  const of = Application("OmniFocus");
  const doc = of.defaultDocument;
  const out = [];
  const walk = (coll, parentName, prefix) => {
    for (const f of coll()) {
      const name = f.name();
      const path = prefix ? prefix + " / " + name : name;
      out.push({id: f.id(), name: name, parent: parentName, path: path});
      walk(f.folders, name, path);
    }
  };
  walk(doc.folders, null, "");
  return JSON.stringify(out);
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
  const containers = parent
    ? [["parent.folders", () => parent.folders], ["parent.sections", () => parent.sections]]
    : [["doc.folders", () => doc.folders], ["doc.sections", () => doc.sections]];
  const errs = [];
  for (const [label, get] of containers) {
    try { get().push(made); return JSON.stringify({ok: true, id: made.id(), name: made.name(), parent: parent ? parent.name() : null, via: label}); }
    catch (e) { errs.push(label + ": " + e); }
  }
  return JSON.stringify({ok: false, error: "could not insert folder — " + errs.join(" | ")});
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
  // Field-found: folder-level containers spell as "sections" — try the
  // plausible destinations in order and report which one took.
  const targets = folder
    ? [["folder.sections.beginning", () => folder.sections.beginning], ["folder.sections.end", () => folder.sections.end], ["folder.projects.beginning", () => folder.projects.beginning]]
    : [["doc.sections.beginning", () => doc.sections.beginning], ["doc.projects.beginning", () => doc.projects.beginning]];
  const errs = [];
  for (const [label, get] of targets) {
    try { of.move(p, {to: get()}); return JSON.stringify({ok: true, name: p.name(), folder: folder ? folder.name() : null, via: label}); }
    catch (e) { errs.push(label + ": " + e); }
  }
  return JSON.stringify({ok: false, error: "move failed — " + errs.join(" | ")});
})()`
