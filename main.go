package main

// ─────────────────────────────────────────────────────────────────────────────
// LESSON 4 — The plumbing is tiny.
//
// Everything main() does:
//   1. build the server, giving it a name and version
//   2. register our tools
//   3. run over the stdio transport until the client hangs up
//
// The transport is the part people find mysterious, and it shouldn't be:
// the Claude desktop app LAUNCHES this binary as a child process and
// speaks JSON-RPC 2.0 to it over stdin/stdout. No network, no ports,
// no auth — the security boundary is simply "it runs as you, on your Mac".
//
// THE ONE RULE THIS IMPLIES: never print to stdout. A single stray
// fmt.Println corrupts the protocol stream and the client drops you.
// All logging goes to stderr (which the desktop app captures to its
// MCP log files — that's where you'll debug).
// ─────────────────────────────────────────────────────────────────────────────

import (
	"context"
	"log"
	"os"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func main() {
	log.SetOutput(os.Stderr) // the rule, enforced
	log.SetPrefix("omnifocus-mcp: ")

	server := mcp.NewServer(&mcp.Implementation{
		Name:    "omnifocus",
		Version: "0.1.0",
	}, nil)

	Register(server, &App{Bridge: OsascriptBridge{}})

	log.Println("starting on stdio")
	if err := server.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		log.Fatalf("server stopped: %v", err)
	}
}
