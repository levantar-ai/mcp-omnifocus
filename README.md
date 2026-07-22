# OmniFocus MCP Server

**Let Claude read and manage your OmniFocus — safely.**

This is a small, open-source [MCP](https://modelcontextprotocol.io) server
that connects the Claude desktop app to OmniFocus on your Mac. Once
installed, you can talk to Claude about your tasks in plain English and it
can see and organise them for you — plan your day, file tasks into
projects, reschedule things, tick things off.

Everything runs **entirely on your Mac**. No cloud service, no account, no
data sent anywhere — Claude talks to a tiny program on your machine, and
that program talks to OmniFocus through Apple's own automation system
(macOS will ask your permission the first time).

## What you can say to Claude

Once installed, things like this just work:

- *"What should I work on today?"*
- *"What's on my plate tomorrow?"*
- *"Add a task to my Renovation project: order paint samples, deferred to Saturday"*
- *"Capture this meeting's actions into OmniFocus under the Acme project"*
- *"Plan my morning — schedule those three tasks with times and durations"*
- *"Push my 2pm focus block to 3pm and shorten it to 45 minutes"*
- *"I've done the invoicing — tick it off"*
- *"Put the Garden project on hold — it's a winter thing"*
- *"Which of my projects have no next action?"*

## What it can do (and what it can't)

Six focused capabilities:

| Tool | What it does |
|---|---|
| `list_projects` | See your projects, their status and defer dates |
| `list_tasks` | See tasks — names, projects, flags, defer/planned/due dates, durations |
| `add_tasks` | Add tasks (TaskPaper format) — into a named project or the inbox, with flags, defer/planned/due dates, durations and notes |
| `update_task` | Change fields on one task — rename, reschedule, flag, set/clear dates, set duration |
| `complete_task` | Mark one task complete |
| `set_project_status` | Set a project to **active** or **on-hold** — nothing else |

## Is it safe? Yes — here's exactly why

- **It cannot delete anything.** There is no delete capability in the code
  at all — not for tasks, not for projects, not for anything.
- **The strongest things it can do are reversible in OmniFocus**: marking
  a task complete (un-completable in the app), and switching a project
  between active and on-hold. It cannot complete or drop *projects* —
  that's deliberately excluded.
- **It only touches OmniFocus.** It has no access to your files, other
  apps, or the internet.
- **It's local and private.** The server is a single program running as
  you, on your Mac, speaking to Claude over a private pipe. Your task data
  never leaves your machine through this tool.
- **macOS guards it.** The first use triggers Apple's automation
  permission prompt — nothing happens until you approve, and you can
  revoke it any time in System Settings → Privacy & Security → Automation.
- **The code is small enough to read.** Six short Go files, heavily
  commented. Don't take our word for any of this — look.

## Install (10 minutes, no coding required)

**You need:** a Mac with [OmniFocus](https://www.omnigroup.com/omnifocus/)
and the [Claude desktop app](https://claude.ai/download).

**1. Get the code.** Either click *Code → Download ZIP* on this repo's
GitHub page and unzip it somewhere permanent (e.g. your home folder), or
in Terminal:

```sh
git clone https://github.com/levantar-ai/mcp-omnifocus.git
```

**2. Install Go** (the free language this is built with). Easiest is the
official installer from [go.dev/dl](https://go.dev/dl/) — download the
macOS Apple Silicon `.pkg` and double-click. (If you use Homebrew:
`brew install go`.)

**3. Run the installer.** In Terminal:

```sh
cd mcp-omnifocus
./install.sh
```

The script builds the server, wires it into your Claude desktop app's
configuration (backing up the existing config first), and tells you
what it did. It changes nothing else.

**4. Restart Claude.** Fully quit (⌘Q) and reopen the Claude desktop app.

**5. Say hello.** In a new chat: *"list my OmniFocus projects."* macOS
will ask whether Claude's helper may control OmniFocus — click **Allow**.
That's it.

## Troubleshooting

**Claude says it has no OmniFocus tools.** The app must be *fully*
restarted (⌘Q, not just closing the window). If it still doesn't appear,
your config file may have a typo — restore the backup the installer made
(same folder, `.backup` in the name) and run `./install.sh` again.

**Every request fails with "not authorised" or similar.** macOS's
automation permission was declined or never shown. Open System Settings →
Privacy & Security → Automation, find the omnifocus-mcp entry, and enable
OmniFocus. Then try again.

**It worked, then stopped after an update.** Re-run `./install.sh` and
restart Claude — a rebuilt binary needs a fresh start.

**Where are the logs?** `~/Library/Logs/Claude/` — each MCP server's
output lands there, and error messages are written to be read.

## Updating

```sh
cd mcp-omnifocus
git pull
./install.sh
```

Then restart Claude. (ZIP users: download the new ZIP over the old folder
and run `./install.sh`.)

## For the curious: this codebase is also a course

The source is written to teach. Six numbered **LESSON** blocks walk
through what an MCP server is and how this one works — read them in this
order: `main.go` (the plumbing), `tools.go` (tools as typed functions),
`bridge.go` (the seam that makes it testable, and the JXA that talks to
OmniFocus), `taskpaper.go` (parse at the boundary you control),
`main_test.go` (what you can test without a Mac). Run the tests with
`go test ./...`.

Built in Go on the official
[MCP Go SDK](https://github.com/modelcontextprotocol/go-sdk). Developed
pair-programming with Claude — including, pleasingly, several bugs found
by running against a real OmniFocus database and fixed with regression
tests.

## Licence

MIT — see [LICENSE](LICENSE). © 2026 Levantar AI Ltd.
