package mcpserver

import (
	"context"
	j "github.com/JesseSenior/codex-thread-bridge/internal/jsonutil"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"os"
	"os/exec"
	"testing"
	"time"
)

// Opt-in read-only check. Never create, resume, steer, or modify a live task.
func TestLiveReadOnly(t *testing.T) {
	socket := os.Getenv("CTB_LIVE_SOCKET")
	if socket == "" {
		t.Skip("set CTB_LIVE_SOCKET for an authorized read-only live check")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.Command(executable, "--socket", socket)
	cmd.Dir = t.TempDir()
	client := mcp.NewClient(&mcp.Implementation{Name: "bridge-read-only-check", Version: "1"}, nil)
	session, e := client.Connect(ctx, &mcp.CommandTransport{Command: cmd}, nil)
	if e != nil {
		t.Fatal(e)
	}
	defer session.Close()
	inventory, e := session.ListTools(ctx, nil)
	if e != nil || len(inventory.Tools) != 10 {
		t.Fatalf("inventory: %v", e)
	}
	listed := tool(t, session, "list_threads", j.Object{"limit": 1})
	rows := j.List(listed["data"])
	t.Logf("MCP connected; listed %d task(s)", len(rows))
	if len(rows) > 0 {
		id := j.Map(rows[0])["id"]
		read := tool(t, session, "read_thread", j.Object{"threadId": id, "turnLimit": 1})
		if j.Map(read["thread"])["id"] != id {
			t.Fatal("read returned another task")
		}
		tool(t, session, "wait_threads", j.Object{"targets": []any{j.Object{"threadId": id}}, "timeoutMs": 0})
		t.Log("Read history and immediate wait completed")
	}
}
