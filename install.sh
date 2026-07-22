#!/bin/bash
# omnifocus-mcp installer — builds the server and wires it into the
# Claude desktop app. Safe to re-run any time (updates included).
set -euo pipefail

cd "$(dirname "$0")"
say() { printf '%s\n' "$*"; }
fail() { printf '\n❌ %s\n' "$*" >&2; exit 1; }

say "── omnifocus-mcp installer ──────────────────────────"

# 1. Sanity checks ----------------------------------------------------------
[ "$(uname)" = "Darwin" ] || fail "This server only runs on macOS (it talks to the OmniFocus app)."

if ! ls /Applications | grep -qi "omnifocus"; then
  say "⚠️  Couldn't see OmniFocus in /Applications — continuing anyway,"
  say "   but the server needs OmniFocus installed to do anything useful."
fi

command -v go >/dev/null 2>&1 || fail "Go isn't installed. Get it from https://go.dev/dl (macOS .pkg, double-click) or 'brew install go', then re-run this script."

# 2. Build ------------------------------------------------------------------
say "• Building the server…"
go build -o omnifocus-mcp . || fail "Build failed — the error above has details. (Nothing was changed.)"
chmod +x omnifocus-mcp
xattr -d com.apple.quarantine omnifocus-mcp 2>/dev/null || true
BINARY="$PWD/omnifocus-mcp"
say "  built: $BINARY"

# 3. Wire into the Claude desktop app --------------------------------------
CONFIG_DIR="$HOME/Library/Application Support/Claude"
CONFIG="$CONFIG_DIR/claude_desktop_config.json"
mkdir -p "$CONFIG_DIR"

if [ -f "$CONFIG" ]; then
  BACKUP="$CONFIG.backup-$(date +%Y%m%d-%H%M%S)"
  cp "$CONFIG" "$BACKUP"
  say "• Backed up your Claude config to:"
  say "  $BACKUP"
fi

say "• Adding the omnifocus server to Claude's config…"
BINARY="$BINARY" CONFIG="$CONFIG" python3 - <<'PY' || fail "Couldn't update the config. If it exists, check it's valid JSON — or restore the backup shown above."
import json, os

config_path = os.environ["CONFIG"]
binary = os.environ["BINARY"]

data = {}
if os.path.exists(config_path):
    with open(config_path) as f:
        content = f.read().strip()
    if content:
        data = json.loads(content)

data.setdefault("mcpServers", {})["omnifocus"] = {"command": binary}

with open(config_path, "w") as f:
    json.dump(data, f, indent=2)
print("  config updated:", config_path)
PY

# 4. Optional starter skill -------------------------------------------------
# A "skill" teaches Claude HOW you like your OmniFocus handled (the server
# only provides the raw ability). We ship a starter template.
SKILL_SRC="$PWD/skill/SKILL.md"
SKILL_DST="$HOME/.claude/skills/omnifocus/SKILL.md"
if [ -f "$SKILL_SRC" ] && [ -t 0 ]; then
  printf '• Install the starter skill for Claude Code/Cowork users? [y/N] '
  read -r REPLY || REPLY=""
  case "$REPLY" in
    [Yy]*)
      mkdir -p "$(dirname "$SKILL_DST")"
      if [ -f "$SKILL_DST" ]; then
        cp "$SKILL_DST" "$SKILL_DST.backup-$(date +%Y%m%d-%H%M%S)"
        say "  (existing skill backed up alongside it)"
      fi
      cp "$SKILL_SRC" "$SKILL_DST"
      say "  starter skill installed: $SKILL_DST"
      say "  Personalise it the easy way — ask Claude:"
      say "  \"Read my omnifocus skill, look at how my OmniFocus is organised,"
      say "   interview me briefly, and personalise it.\""
      ;;
    *) say "  skipped — see README ('Teach Claude your conventions') to add it later." ;;
  esac
fi

# 5. Done -------------------------------------------------------------------
say ""
say "✅ Installed. Two steps left, and they're yours:"
say ""
say "  1. Fully quit the Claude app (⌘Q) and reopen it."
say "  2. In a new chat, say:  \"list my OmniFocus projects\""
say "     macOS will ask permission to control OmniFocus — click Allow."
say ""
say "If anything misbehaves, see the Troubleshooting section in README.md."
