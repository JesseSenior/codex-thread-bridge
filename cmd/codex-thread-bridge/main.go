// codex-thread-bridge manages tasks through an existing local App Server.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/JesseSenior/codex-thread-bridge/internal/bridge"
	"github.com/JesseSenior/codex-thread-bridge/internal/mcpserver"
	"github.com/JesseSenior/codex-thread-bridge/internal/rpc"
	"github.com/JesseSenior/codex-thread-bridge/internal/version"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
)

const help = `usage: codex-thread-bridge [-h] [--version] [--socket PATH] [serve|diagnose]

Manage tasks on one existing App Server. No daemon startup or bridge database.

  serve          Run the MCP stdio server (default)
  diagnose       Print read-only connection diagnostics
  --socket PATH  Existing App Server Unix WebSocket socket
  --version      Print the version
  -h, --help     Show this help
`

func run(ctx context.Context, args []string) error {
	home, ok := os.LookupEnv("CODEX_HOME")
	if !ok {
		h, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		home = filepath.Join(h, ".codex")
	}
	socket := filepath.Join(home, "app-server-control", "app-server-control.sock")
	command := "serve"
	hasCommand := false
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--help" || arg == "-h":
			fmt.Print(help)
			return nil
		case arg == "--version":
			fmt.Println(version.Version)
			return nil
		case arg == "--socket":
			i++
			if i == len(args) {
				return fmt.Errorf("--socket requires a path")
			}
			socket = args[i]
		case strings.HasPrefix(arg, "--socket="):
			socket = strings.TrimPrefix(arg, "--socket=")
		case (arg == "serve" || arg == "diagnose") && !hasCommand:
			command = arg
			hasCommand = true
		default:
			return fmt.Errorf("unrecognized arguments: %s", arg)
		}
	}
	client := rpc.New(socket)
	defer client.Close()
	b := bridge.New(client)
	if command == "diagnose" {
		v, err := b.Diagnostics(ctx)
		if err != nil {
			return err
		}
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")
		return encoder.Encode(v)
	}
	server, err := mcpserver.New(b)
	if err != nil {
		return err
	}
	return server.Run(ctx, &mcp.StdioTransport{})
}
func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
