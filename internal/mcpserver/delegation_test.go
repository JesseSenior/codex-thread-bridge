package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	j "github.com/JesseSenior/codex-thread-bridge/internal/jsonutil"
	"github.com/JesseSenior/codex-thread-bridge/internal/testserver"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func callWithMeta(s *mcp.ClientSession, name string, args j.Object, meta mcp.Meta) (j.Object, error) {
	r, err := s.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args, Meta: meta})
	if err != nil {
		return nil, err
	}
	if r.IsError {
		return nil, fmt.Errorf("%s: %v", name, r.Content)
	}
	raw, err := json.Marshal(r.StructuredContent)
	if err != nil {
		return nil, err
	}
	var out j.Object
	err = j.Decode(raw, &out)
	return out, err
}

func TestRequestMetadataIsolation(t *testing.T) {
	t.Setenv("CODEX_THREAD_ID", "wrong-process-source")
	f := testserver.Start(t)
	s := connect(t, f, filepath.Join(t.TempDir(), "absent"))
	dir := t.TempDir()
	r, err := callWithMeta(s, "create_thread", j.Object{"cwd": dir, "prompt": "first"}, mcp.Meta{"threadId": "caller-one"})
	if err != nil {
		t.Fatal(err)
	}
	id := r["threadId"]
	if _, err = callWithMeta(s, "send_message_to_thread", j.Object{"threadId": id, "prompt": "second"}, mcp.Meta{"threadId": "caller-two"}); err != nil {
		t.Fatal(err)
	}
	tool(t, s, "send_message_to_thread", j.Object{"threadId": id, "prompt": "ordinary"})
	history := tool(t, s, "read_thread", j.Object{"threadId": id})
	turns := j.List(history["turns"])
	for i, source := range []any{nil, "caller-two", "caller-one"} {
		message := j.Map(j.List(j.Map(turns[i])["messages"])[0])
		if !j.Equal(message["sourceThreadId"], source) {
			t.Fatalf("source leaked between requests: %v", message)
		}
	}
	if strings.Contains(j.PythonJSON(history), "wrong-process-source") {
		t.Fatal("used process environment as caller")
	}

	// All callers share one MCP process and client session.
	type result struct {
		source string
		id     any
		err    error
	}
	results := make(chan result, 6)
	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			source := fmt.Sprintf("concurrent-caller-%d", i)
			r, err := callWithMeta(s, "create_thread", j.Object{"cwd": dir, "prompt": source}, mcp.Meta{"threadId": source})
			results <- result{source, r["threadId"], err}
		}()
	}
	wg.Wait()
	close(results)
	for result := range results {
		if result.err != nil {
			t.Fatal(result.err)
		}
		history := tool(t, s, "read_thread", j.Object{"threadId": result.id})
		message := j.Map(j.List(j.Map(j.List(history["turns"])[0])["messages"])[0])
		if message["sourceThreadId"] != result.source || message["text"] != result.source {
			t.Fatalf("caller attribution crossed requests: %v", message)
		}
	}
}

func TestInvalidSourceMetadataBeforeRPC(t *testing.T) {
	f := testserver.Start(t)
	s := connect(t, f, filepath.Join(t.TempDir(), "absent"))
	for _, source := range []any{nil, "", " \n", 123, true, j.Object{"threadId": "nested"}, strings.Repeat("a", 129)} {
		for name, args := range map[string]j.Object{
			"create_thread":          {"cwd": t.TempDir()},
			"send_message_to_thread": {"threadId": "target", "prompt": "hello"},
		} {
			_, err := callWithMeta(s, name, args, mcp.Meta{"threadId": source})
			if err == nil {
				t.Fatalf("accepted invalid metadata: %v", source)
			}
		}
	}
	if f.Count("initialize") != 0 {
		t.Fatal("invalid source metadata caused RPC")
	}
}
