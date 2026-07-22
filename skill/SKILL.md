---
name: omnifocus
description: How to work with this user's OmniFocus via the omnifocus MCP tools (list_projects, list_tasks, add_tasks, update_task, complete_task, set_project_status, list_folders, create_folder, rename_folder, move_project). Use whenever OmniFocus comes up, and also when the user asks what to work on, wants a plan or meeting outcomes captured as tasks, says "add this to my tasks", asks to tick something off, or wants help prioritising, focusing or planning their week — even if they don't name OmniFocus explicitly.
---

<!--
  STARTER SKILL — personalise me!
  The MCP server gives Claude the ABILITY to work your OmniFocus; this
  skill gives it the JUDGEMENT to do it your way. Everything marked
  ✏️ PERSONALISE is a placeholder. The easiest way to fill them in:
  once the server is installed, ask Claude —

    "Read the omnifocus skill template, look at how my OmniFocus is
     actually organised, interview me briefly, and produce my
     personalised version."

  Then save the result as your skill. The unmarked rules are universal —
  keep them.
-->

# Working with my OmniFocus

The omnifocus MCP server exposes ten tools: `list_projects`, `list_tasks`,
`add_tasks` (TaskPaper in), `update_task`, `complete_task`,
`set_project_status`, `list_folders`, `create_folder`, `rename_folder`,
`move_project`. This skill is the judgement layer: my structure and
conventions, so changes land the way I'd have made them.

## My structure

✏️ PERSONALISE — describe how your database is organised, e.g.:
folders per area of life/work; whether you prefer many small projects or
fewer big ones; any naming conventions.

## Adding tasks

- File tasks under an **existing** project with the `project` parameter,
  using the exact name from `list_projects`. If the tool reports
  `projectsNotFound`, list projects and retry with exact names — never
  let tasks land in the inbox by accident.
- `@flagged`, `@defer(YYYY-MM-DD HH:MM)`, `@due(...)`, `@planned(...)`
  and `@estimate(30m|2h|1h30m)` become real task properties; indented
  lines beneath a task become its note.
- Scheduling semantics: `@defer` = when it becomes *available*,
  `@planned` = when I *intend to do it*, `@estimate` = honest duration.
  Don't abuse defer as a scheduler; don't pad estimates.
- ✏️ PERSONALISE — your tagging/flag conventions, e.g. how sparingly
  flags are used and what they mean to you.
- No due dates unless a real external deadline exists. Never invent
  dates to seem organised.
- Check for existing tasks before adding — duplicates are worse than gaps.

## Folders

- Folders are areas, not projects — check `list_folders` before creating
  one, and prefer filing into an existing folder over inventing a new
  one. Duplicate names are allowed by OmniFocus but rarely wanted.
- New projects created via `add_tasks` land at the top level; use
  `move_project` to file them where they belong.
- ✏️ PERSONALISE — your folder taxonomy: what the top-level folders
  mean, when a new one is justified, any nesting conventions.
- Folders can be created, renamed and filled — never deleted or hidden
  through these tools. Treat renames carefully: other automations may
  match on folder names.

## Reading and reviewing

- "What should I work on?" → flagged tasks first, then what's truly
  available now.
- **Defer dates schedule projects**: an "active" project deferred into
  the future is not live yet. Bucket accordingly when listing: available
  now · tomorrow · later · on hold.
- ✏️ PERSONALISE — bucket names and any housekeeping projects to hide
  (e.g. plugin/preference-sync projects that aren't real work).

## Prioritisation reviews

When asked to prioritise or plan the week, run a **conversation, not a
verdict**:

1. Fresh data first; show upcoming projects compactly, flagging any with
   no defined next task (define one before ranking it).
2. Hard external deadlines get scheduled before preferences, no debate.
3. Rank by forced choice, two or three questions at a time — "if only
   two of these move this week, which?" — not a scored list.
4. Sum estimates against realistic capacity; if the plan doesn't fit,
   say so plainly and help cut. Helping someone overcommit is failing
   them.
5. Apply decisions: reschedule a project via `update_task` on the
   project's own id (project rows appear in `list_tasks`); park via
   `set_project_status` on-hold. Confirm before applying more than a
   handful of changes.

## Updating, completing — safety rules (keep these)

- `update_task` and `complete_task` only with ids from a **fresh**
  `list_tasks` call; confirm with the user if the target is at all
  ambiguous. Never guess ids; never complete in bulk without listing
  what will be ticked.
- Prefer `update_task` over recreate-and-complete when rescheduling.
- Nothing here can delete; keep it that way in spirit — treat
  completion and status changes as the serious actions they are.
