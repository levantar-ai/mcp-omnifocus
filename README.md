# omnifocus-mcp

A local MCP server, in Go, that lets Claude read and write your OmniFocus —
and a teaching project: the five numbered LESSON blocks in the source are
the course, in order.

## What an MCP server actually is (60 seconds)

The Claude desktop app launches this binary as a child process and speaks
**JSON-RPC 2.0 over stdin/stdout** to it. On startup they shake hands; the
server declares its **tools** (name + description + JSON Schema for inputs);
thereafter the model calls tools and receives results. That's the whole
protocol as far as a simple server is concerned.

Consequences worth internalising:

- **stdout is sacred.** It carries the protocol. Log to stderr only.
- **The model reads your descriptions.** Tool and field descriptions are the
  API docs the model uses to decide when and how to call you — write them well.
- **Local stdio servers have no auth** because the boundary is the process
  boundary: it runs as you, on your machine, launched only by your client.

## Reading order

1. `main.go` — the plumbing (server, transport, the stdout rule)
2. `tools.go` — tools as typed functions; schema from struct tags; in-band errors
3. `bridge.go` — the seam that makes it testable; the JXA scripts
4. `main_test.go` — what you can test off-Mac, and where that boundary sits

## Build & run on your Mac

```sh
brew install go            # if needed
cd omnifocus-mcp
go build -o omnifocus-mcp .
```

**Verify the JXA first** (the honest step): open Script Editor, set the
language dropdown to JavaScript, and run the body of `jxaListProjects` from
`bridge.go`. Then try `jxaAddTasks` with a throwaway task. The two commands
marked `⚠ VERIFY-ON-MAC` (`parseTasksInto`, `markComplete`) may need their
spelling adjusted to your OmniFocus version — Script Editor's error messages
will tell you. First run will trigger a macOS automation-permission prompt
(System Settings → Privacy & Security → Automation) — approve it, or every
call fails with a permissions error on stderr.

## Wire into the Claude desktop app

Edit `~/Library/Application Support/Claude/claude_desktop_config.json`:

```json
{
  "mcpServers": {
    "omnifocus": {
      "command": "/absolute/path/to/omnifocus-mcp"
    }
  }
}
```

Restart the desktop app; the tools appear in Claude's tool list. Debugging
loop: the app writes each server's stderr to its MCP logs
(`~/Library/Logs/Claude/`) — your `log.Println`s land there.

## Exercises (in ascending order of fun)

1. Add a `dueWithinDays` filter to `list_tasks` — one struct field, one
   script edit, one test.
2. Add a `list_flagged_by_project` convenience tool. Notice the design
   question: is a new tool better than another filter? (The model handles
   few-well-described tools better than many overlapping ones.)
3. Make `add_tasks` return the created tasks' ids, not just a count.
4. Add an MCP **resource** (`taskpaper://inbox`) exposing the inbox as
   readable context — resources are the half of MCP this project hasn't
   touched yet, and the SDK docs' `AddResource` is the door in.

## Provenance

Scaffolded pair-programming with Claude (which cannot run OmniFocus and
therefore mocked it — see LESSON 5 for why that constraint improved the
design). SDK: github.com/modelcontextprotocol/go-sdk v1.0.0 (pinned for
Go 1.24; if your Mac has Go ≥1.25, `go get` the latest and nothing here
should need to change).
