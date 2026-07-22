package main

// ─────────────────────────────────────────────────────────────────────────────
// LESSON 5 — Testing without a Mac.
//
// The fake bridge records the script it was asked to run and returns a
// canned response. That lets us verify, on any machine:
//   • handlers pass user input into scripts JSON-encoded (injection-safe)
//   • bridge failures come back as in-band tool errors, not crashes
//   • input validation fires before we ever shell out
//
// What this deliberately does NOT test is the JXA itself — that's the
// verify-on-Mac step in the README. Knowing where your test boundary
// sits is most of the skill.
// ─────────────────────────────────────────────────────────────────────────────

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type fakeBridge struct {
	lastScript string
	reply      string
	err        error
}

func (f *fakeBridge) RunJXA(ctx context.Context, script string) (string, error) {
	f.lastScript = script
	return f.reply, f.err
}

func text(res *mcp.CallToolResult) string {
	if len(res.Content) == 0 {
		return ""
	}
	if tc, ok := res.Content[0].(*mcp.TextContent); ok {
		return tc.Text
	}
	return ""
}

func TestListTasksEncodesFilters(t *testing.T) {
	fb := &fakeBridge{reply: `[]`}
	app := &App{Bridge: fb}

	res, _, err := app.ListTasks(context.Background(), nil, ListTasksInput{
		Project:     `Tripwires "SharePoint" Planting`, // hostile quotes on purpose
		FlaggedOnly: true,
	})
	if err != nil {
		t.Fatalf("unexpected protocol error: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected tool error: %s", text(res))
	}
	// The project name must appear JSON-escaped inside the script —
	// raw quotes would mean we're splicing code, not data.
	if !strings.Contains(fb.lastScript, `\"SharePoint\"`) {
		t.Errorf("project name not JSON-encoded in script:\n%s", fb.lastScript)
	}
	if !strings.Contains(fb.lastScript, "true") {
		t.Errorf("flaggedOnly not passed through")
	}
}

func TestParseTaskPaperFull(t *testing.T) {
	// Regression suite for the transport-text bug: annotations must become
	// structured fields, never survive inside names.
	tp := "- Tripwires\n" +
		"\t- Tripwires SharePoint Planting\n" +
		"\t\t- Write the design doc @parallel(true) @autodone(false) @flagged\n" +
		"\t\t\tReview with Andy first.\n" +
		"\t\t- Review Graph scopes @defer(2026-07-23 09:00) @due(2026-07-25 17:00)\n" +
		"- Email AdiShaktiPuja@syuk.org about setup\n"

	groups := ParseTaskPaper(tp, "")
	if len(groups) != 2 {
		t.Fatalf("want 2 groups (project + inbox), got %d: %+v", len(groups), groups)
	}

	proj := groups[0]
	if proj.Project != "Tripwires SharePoint Planting" {
		t.Errorf("innermost container should be the project, got %q", proj.Project)
	}
	if len(proj.Tasks) != 2 {
		t.Fatalf("want 2 project tasks, got %+v", proj.Tasks)
	}
	first := proj.Tasks[0]
	if first.Name != "Write the design doc" || !first.Flagged || first.Note != "Review with Andy first." {
		t.Errorf("annotation extraction failed: %+v", first)
	}
	second := proj.Tasks[1]
	if second.Defer != "2026-07-23T09:00" || second.Due != "2026-07-25T17:00" {
		t.Errorf("date normalisation failed: %+v", second)
	}
	if strings.Contains(second.Name, "@") {
		t.Errorf("annotations leaked into name: %q", second.Name)
	}

	inbox := groups[1]
	if inbox.Project != "" || len(inbox.Tasks) != 1 {
		t.Fatalf("want 1 inbox task, got %+v", inbox)
	}
	// The '@' in an email address is NOT an annotation and must survive.
	if inbox.Tasks[0].Name != "Email AdiShaktiPuja@syuk.org about setup" {
		t.Errorf("legitimate @ mangled: %q", inbox.Tasks[0].Name)
	}
}

func TestParsePlannedAndEstimate(t *testing.T) {
	tp := "- Deep work block @planned(2026-07-23 09:00) @estimate(1h30m)\n" +
		"- Quick call @plan(2026-07-23 14:00) @duration(30m)\n"
	groups := ParseTaskPaper(tp, "")
	if len(groups) != 1 || len(groups[0].Tasks) != 2 {
		t.Fatalf("unexpected parse: %+v", groups)
	}
	a, b := groups[0].Tasks[0], groups[0].Tasks[1]
	if a.Planned != "2026-07-23T09:00" || a.EstimateMin != 90 {
		t.Errorf("planned/estimate wrong: %+v", a)
	}
	if b.Planned != "2026-07-23T14:00" || b.EstimateMin != 30 {
		t.Errorf("@plan/@duration aliases wrong: %+v", b)
	}
	for _, task := range groups[0].Tasks {
		if strings.Contains(task.Name, "@") {
			t.Errorf("annotation leaked into name: %q", task.Name)
		}
	}
}

func TestParseEstimateUnits(t *testing.T) {
	cases := map[string]int{"30m": 30, "2h": 120, "1h30m": 90, "90": 90, "1hr": 60, "45 mins": 45, "junk": 0}
	for in, want := range cases {
		if got := parseEstimate(in); got != want {
			t.Errorf("parseEstimate(%q) = %d, want %d", in, got, want)
		}
	}
}

func TestAddTasksUsesDefaultProject(t *testing.T) {
	fb := &fakeBridge{reply: `{"created":1,"projectsNotFound":[]}`}
	app := &App{Bridge: fb}

	_, _, _ = app.AddTasks(context.Background(), nil, AddTasksInput{
		TaskPaper: "- lone task",
		Project:   "Complete CMS Discovery SOW",
	})
	if !strings.Contains(fb.lastScript, `"Complete CMS Discovery SOW"`) {
		t.Errorf("default project not applied:\n%s", fb.lastScript)
	}
	if !strings.Contains(fb.lastScript, `"lone task"`) {
		t.Errorf("task name mangled:\n%s", fb.lastScript)
	}
}

func TestAddTasksRejectsEmptyInput(t *testing.T) {
	fb := &fakeBridge{}
	app := &App{Bridge: fb}

	res, _, _ := app.AddTasks(context.Background(), nil, AddTasksInput{})
	if !res.IsError {
		t.Fatal("empty taskpaper should produce an in-band error")
	}
	if fb.lastScript != "" {
		t.Fatal("should not have shelled out for invalid input")
	}
}

func TestBridgeFailureIsInBand(t *testing.T) {
	fb := &fakeBridge{err: errors.New("osascript failed: not authorised")}
	app := &App{Bridge: fb}

	res, _, err := app.ListProjects(context.Background(), nil, ListProjectsInput{})
	if err != nil {
		t.Fatalf("bridge failure must not become a protocol error, got: %v", err)
	}
	if !res.IsError || !strings.Contains(text(res), "not authorised") {
		t.Fatalf("expected in-band error carrying the cause, got: %s", text(res))
	}
}

func TestBuildPatch(t *testing.T) {
	flag := true
	zero := 0
	in := UpdateTaskInput{
		ID:          "abc",
		Planned:     "2026-07-24 09:00",
		Defer:       "clear",
		Flagged:     &flag,
		EstimateMin: &zero,
	}
	p := buildPatch(in)
	if p["planned"] != "2026-07-24T09:00" {
		t.Errorf("planned not normalised: %v", p["planned"])
	}
	if v, ok := p["defer"]; !ok || v != nil {
		t.Errorf("'clear' should map to explicit null, got %v (present=%v)", v, ok)
	}
	if p["flagged"] != true {
		t.Errorf("flagged pointer not applied")
	}
	if v, ok := p["estimateMin"]; !ok || v != nil {
		t.Errorf("estimate 0 should clear, got %v (present=%v)", v, ok)
	}
	if _, ok := p["due"]; ok {
		t.Errorf("unset field leaked into patch")
	}
	if _, ok := p["name"]; ok {
		t.Errorf("unset name leaked into patch")
	}
}

func TestUpdateTaskValidation(t *testing.T) {
	fb := &fakeBridge{}
	app := &App{Bridge: fb}

	res, _, _ := app.UpdateTask(context.Background(), nil, UpdateTaskInput{})
	if !res.IsError {
		t.Fatal("missing id must error in-band")
	}
	res, _, _ = app.UpdateTask(context.Background(), nil, UpdateTaskInput{ID: "abc"})
	if !res.IsError {
		t.Fatal("empty patch must error in-band")
	}
	if fb.lastScript != "" {
		t.Fatal("must not shell out on invalid input")
	}
}

func TestSetProjectStatusValidation(t *testing.T) {
	fb := &fakeBridge{}
	app := &App{Bridge: fb}

	res, _, _ := app.SetProjectStatus(context.Background(), nil, SetProjectStatusInput{Status: "active"})
	if !res.IsError {
		t.Fatal("missing id must error in-band")
	}
	res, _, _ = app.SetProjectStatus(context.Background(), nil, SetProjectStatusInput{ID: "abc", Status: "dropped"})
	if !res.IsError {
		t.Fatal("unsupported status must error in-band — this tool must not drop projects")
	}
	if fb.lastScript != "" {
		t.Fatal("must not shell out on invalid input")
	}
}

func TestSetProjectStatusNormalisesAndEncodes(t *testing.T) {
	fb := &fakeBridge{reply: `{"ok":true,"name":"Lakes 2026","status":"on-hold"}`}
	app := &App{Bridge: fb}

	res, _, _ := app.SetProjectStatus(context.Background(), nil, SetProjectStatusInput{
		ID:     `p"quote`, // hostile quotes on purpose
		Status: "On Hold", // friendly spelling must normalise to on-hold
	})
	if res.IsError {
		t.Fatalf("unexpected tool error: %s", text(res))
	}
	if !strings.Contains(fb.lastScript, `\"quote`) {
		t.Errorf("project id not JSON-encoded in script:\n%s", fb.lastScript)
	}
	if !strings.Contains(fb.lastScript, `"on-hold"`) {
		t.Errorf("status not normalised to on-hold:\n%s", fb.lastScript)
	}
}

// ── Folder tools (v0.7) ──────────────────────────────────────────────────────

func TestCreateFolderValidatesAndEncodes(t *testing.T) {
	fb := &fakeBridge{reply: `{"ok":true,"id":"f1","name":"Clients","parent":null}`}
	app := &App{Bridge: fb}

	// Empty (and whitespace-only) names must never reach OmniFocus.
	res, _, _ := app.CreateFolder(context.Background(), nil, CreateFolderInput{Name: "   "})
	if !res.IsError {
		t.Fatal("blank folder name must error in-band")
	}
	if fb.lastScript != "" {
		t.Fatal("must not shell out on invalid input")
	}

	// A hostile name must arrive JSON-escaped, and no parent must arrive
	// as a literal null the JXA side can branch on.
	res, _, _ = app.CreateFolder(context.Background(), nil, CreateFolderInput{Name: `Clients "A" & co`})
	if res.IsError {
		t.Fatalf("unexpected tool error: %s", text(res))
	}
	if !strings.Contains(fb.lastScript, `\"A\"`) {
		t.Errorf("folder name not JSON-encoded in script:\n%s", fb.lastScript)
	}
	if !strings.Contains(fb.lastScript, "const parentId = null") {
		t.Errorf("missing parent should splice as null:\n%s", fb.lastScript)
	}

	// With a parent, the id must be passed through encoded.
	_, _, _ = app.CreateFolder(context.Background(), nil, CreateFolderInput{Name: "Subarea", ParentID: "pf-123"})
	if !strings.Contains(fb.lastScript, `"pf-123"`) {
		t.Errorf("parent id not passed through:\n%s", fb.lastScript)
	}
}

func TestRenameFolderValidation(t *testing.T) {
	fb := &fakeBridge{}
	app := &App{Bridge: fb}

	res, _, _ := app.RenameFolder(context.Background(), nil, RenameFolderInput{Name: "New name"})
	if !res.IsError {
		t.Fatal("missing id must error in-band")
	}
	res, _, _ = app.RenameFolder(context.Background(), nil, RenameFolderInput{ID: "f1"})
	if !res.IsError {
		t.Fatal("missing name must error in-band")
	}
	if fb.lastScript != "" {
		t.Fatal("must not shell out on invalid input")
	}
}

func TestMoveProjectEncodesAndDefaultsToTopLevel(t *testing.T) {
	fb := &fakeBridge{reply: `{"ok":true,"name":"Renovation","folder":null}`}
	app := &App{Bridge: fb}

	res, _, _ := app.MoveProject(context.Background(), nil, MoveProjectInput{})
	if !res.IsError {
		t.Fatal("missing projectId must error in-band")
	}
	if fb.lastScript != "" {
		t.Fatal("must not shell out on invalid input")
	}

	// Omitted folderId means "top level" — the JXA side sees null.
	res, _, _ = app.MoveProject(context.Background(), nil, MoveProjectInput{ProjectID: `p"quote`})
	if res.IsError {
		t.Fatalf("unexpected tool error: %s", text(res))
	}
	if !strings.Contains(fb.lastScript, `\"quote`) {
		t.Errorf("project id not JSON-encoded in script:\n%s", fb.lastScript)
	}
	if !strings.Contains(fb.lastScript, "const folderId = null") {
		t.Errorf("omitted folder should splice as null:\n%s", fb.lastScript)
	}

	_, _, _ = app.MoveProject(context.Background(), nil, MoveProjectInput{ProjectID: "p1", FolderID: "f9"})
	if !strings.Contains(fb.lastScript, `"f9"`) {
		t.Errorf("folder id not passed through:\n%s", fb.lastScript)
	}
}

func TestServerRegistersAllTools(t *testing.T) {
	// Build a real server around the fake bridge and check registration
	// succeeds — this catches schema-inference errors in input structs
	// (the SDK panics at AddTool time if a struct can't become a schema).
	server := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "0.0.0"}, nil)
	Register(server, &App{Bridge: &fakeBridge{reply: "[]"}})
}
