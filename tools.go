package main

// ─────────────────────────────────────────────────────────────────────────────
// LESSON 3 — Tools are typed functions with schemas.
//
// This is the heart of MCP. A tool is three things:
//   1. a name + description   (how the model decides WHEN to call it)
//   2. an input schema        (how the model knows WHAT to send)
//   3. a handler              (what actually happens)
//
// The Go SDK generates the JSON Schema for (2) from your input struct —
// the `json` tag names the field, the `jsonschema` tag describes it.
// Those descriptions are not decoration: the model reads them when
// deciding how to call your tool. Write them like you'd write for a
// sharp colleague who can't see your code.
// ─────────────────────────────────────────────────────────────────────────────

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// App carries dependencies for every handler. Injecting the Bridge here
// (rather than using a global) is what makes the fake-bridge tests work.
type App struct {
	Bridge Bridge
}

// ── Tool inputs ──────────────────────────────────────────────────────────────
// Each tool gets a struct, even the empty one — the SDK requires an
// object-typed input schema, and an explicit struct keeps the door open
// for future fields without breaking existing callers.

type ListProjectsInput struct {
	IncludeAll bool `json:"includeAll,omitempty" jsonschema:"include on-hold, done and dropped projects; by default only active projects are returned"`
}

type ListTasksInput struct {
	Project     string `json:"project,omitempty" jsonschema:"only return tasks in the project with this exact name"`
	FlaggedOnly bool   `json:"flaggedOnly,omitempty" jsonschema:"only return flagged tasks"`
}

type AddTasksInput struct {
	TaskPaper string `json:"taskpaper" jsonschema:"tasks in TaskPaper format: '- task' lines with optional @flagged, @defer(YYYY-MM-DD HH:MM), @due(...), @planned(...) for the intended do-date, @estimate(30m|2h|1h30m) for duration, indented note lines beneath, and container lines to file tasks under a project; @parallel/@autodone annotations are accepted and ignored on tasks"`
	Project   string `json:"project,omitempty" jsonschema:"file tasks under this EXISTING project (exact name from list_projects); tasks under their own container lines in the taskpaper override this; omit for inbox"`
}

type CompleteTaskInput struct {
	ID string `json:"id" jsonschema:"the OmniFocus task id, as returned by list_tasks"`
}

type UpdateTaskInput struct {
	ID          string `json:"id" jsonschema:"the OmniFocus task id, as returned by a FRESH list_tasks call"`
	Name        string `json:"name,omitempty" jsonschema:"rename the task"`
	Note        string `json:"note,omitempty" jsonschema:"replace the task's note"`
	Flagged     *bool  `json:"flagged,omitempty" jsonschema:"set or clear the flag"`
	Defer       string `json:"defer,omitempty" jsonschema:"new defer date-time (YYYY-MM-DD HH:MM), or the word clear to remove it"`
	Due         string `json:"due,omitempty" jsonschema:"new due date-time, or clear"`
	Planned     string `json:"planned,omitempty" jsonschema:"new planned (intended do-date) date-time, or clear"`
	EstimateMin *int   `json:"estimateMin,omitempty" jsonschema:"estimated duration in minutes; 0 clears it"`
}

// buildPatch turns the sparse input into the patch object the JXA side
// applies. The convention: a field absent from the patch is untouched;
// null clears. This asymmetric shape is why the input uses pointers and
// the "clear" sentinel — JSON can't otherwise distinguish "unset" from
// "set to nothing".
func buildPatch(in UpdateTaskInput) map[string]any {
	patch := map[string]any{}
	if in.Name != "" {
		patch["name"] = in.Name
	}
	if in.Note != "" {
		patch["note"] = in.Note
	}
	if in.Flagged != nil {
		patch["flagged"] = *in.Flagged
	}
	setDate := func(key, v string) {
		if v == "" {
			return
		}
		if v == "clear" {
			patch[key] = nil
		} else {
			patch[key] = normDate(v)
		}
	}
	setDate("defer", in.Defer)
	setDate("due", in.Due)
	setDate("planned", in.Planned)
	if in.EstimateMin != nil {
		if *in.EstimateMin == 0 {
			patch["estimateMin"] = nil
		} else {
			patch["estimateMin"] = *in.EstimateMin
		}
	}
	return patch
}

// ── Handlers ─────────────────────────────────────────────────────────────────
// A pattern worth copying: every handler is a thin wrapper around one
// bridge call, and errors are RETURNED IN-BAND as tool results rather
// than as protocol errors. The model can read an in-band error and adapt
// ("no task with that id — let me list tasks first"); a protocol error
// just aborts the exchange.

func textResult(s string) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: s}}}
}

func errResult(err error) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: "error: " + err.Error()}},
		IsError: true,
	}
}

// mustJSON encodes a value for splicing into a JXA script. Encoding user
// input as JSON before it enters the script is what prevents a task name
// like `"); doSomethingEvil(); ("` from becoming executable code.
func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func (a *App) ListProjects(ctx context.Context, req *mcp.CallToolRequest, in ListProjectsInput) (*mcp.CallToolResult, any, error) {
	script := fmt.Sprintf(jxaListProjects, mustJSON(in.IncludeAll))
	out, err := a.Bridge.RunJXA(ctx, script)
	if err != nil {
		return errResult(err), nil, nil
	}
	return textResult(out), nil, nil
}

func (a *App) ListTasks(ctx context.Context, req *mcp.CallToolRequest, in ListTasksInput) (*mcp.CallToolResult, any, error) {
	var project any // null when unset, so the JXA side can distinguish
	if in.Project != "" {
		project = in.Project
	}
	script := fmt.Sprintf(jxaListTasks, mustJSON(project), mustJSON(in.FlaggedOnly))
	out, err := a.Bridge.RunJXA(ctx, script)
	if err != nil {
		return errResult(err), nil, nil
	}
	return textResult(out), nil, nil
}

func (a *App) AddTasks(ctx context.Context, req *mcp.CallToolRequest, in AddTasksInput) (*mcp.CallToolResult, any, error) {
	if in.TaskPaper == "" {
		return errResult(fmt.Errorf("taskpaper must not be empty")), nil, nil
	}
	groups := ParseTaskPaper(in.TaskPaper, in.Project)
	if len(groups) == 0 {
		return errResult(fmt.Errorf("no tasks found in the taskpaper block")), nil, nil
	}
	script := fmt.Sprintf(jxaAddTasks, mustJSON(groups))
	out, err := a.Bridge.RunJXA(ctx, script)
	if err != nil {
		return errResult(err), nil, nil
	}
	return textResult(out), nil, nil
}

func (a *App) CompleteTask(ctx context.Context, req *mcp.CallToolRequest, in CompleteTaskInput) (*mcp.CallToolResult, any, error) {
	if in.ID == "" {
		return errResult(fmt.Errorf("id must not be empty")), nil, nil
	}
	script := fmt.Sprintf(jxaCompleteTask, mustJSON(in.ID))
	out, err := a.Bridge.RunJXA(ctx, script)
	if err != nil {
		return errResult(err), nil, nil
	}
	return textResult(out), nil, nil
}

func (a *App) UpdateTask(ctx context.Context, req *mcp.CallToolRequest, in UpdateTaskInput) (*mcp.CallToolResult, any, error) {
	if in.ID == "" {
		return errResult(fmt.Errorf("id must not be empty")), nil, nil
	}
	patch := buildPatch(in)
	if len(patch) == 0 {
		return errResult(fmt.Errorf("no fields to update were provided")), nil, nil
	}
	script := fmt.Sprintf(jxaUpdateTask, mustJSON(in.ID), mustJSON(patch))
	out, err := a.Bridge.RunJXA(ctx, script)
	if err != nil {
		return errResult(err), nil, nil
	}
	return textResult(out), nil, nil
}

// Register declares the tools on the server. Kept separate from
// main() so tests can build an identical server around a fake bridge.
func Register(server *mcp.Server, app *App) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "list_projects",
		Description: "List OmniFocus projects with ids, names, statuses and defer dates (ISO 8601, null if none). Active-status projects with a future defer date are scheduled, not currently live — treat the defer date as authoritative for availability.",
	}, app.ListProjects)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "list_tasks",
		Description: "List incomplete OmniFocus tasks, optionally filtered to one project or to flagged tasks only. Returns id, name, project, flagged, due, defer, planned date and estimated minutes for each (null where unset or unsupported).",
	}, app.ListTasks)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "add_tasks",
		Description: "Add tasks to OmniFocus from TaskPaper. Supports @flagged, @defer(datetime), @due(datetime) and indented notes as real task properties, and files tasks under an existing project via the project parameter or container lines. Reports any project names that don't exist instead of misfiling — on that error, call list_projects and retry with exact names.",
	}, app.AddTasks)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "update_task",
		Description: "Update fields on an existing OmniFocus task by id: rename, note, flag, defer/planned/due date-times (pass the word clear to remove one), estimated minutes (0 clears). Only provided fields change. Prefer this over recreate-and-complete when rescheduling.",
	}, app.UpdateTask)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "complete_task",
		Description: "Mark a single OmniFocus task complete, identified by the id returned from list_tasks.",
	}, app.CompleteTask)
}
