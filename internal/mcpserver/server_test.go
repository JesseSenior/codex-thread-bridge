package mcpserver

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	j "github.com/JesseSenior/codex-thread-bridge/internal/jsonutil"
	"github.com/JesseSenior/codex-thread-bridge/internal/testserver"
	"github.com/JesseSenior/codex-thread-bridge/internal/version"
	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

//go:embed testdata/tools.json
var referenceTools []byte

var executable string

func TestMain(m *testing.M) {
	executable = os.Getenv("CTB_TEST_BINARY")
	if executable != "" {
		os.Exit(m.Run())
	}
	dir, e := os.MkdirTemp("", "ctb-bin-")
	if e != nil {
		fmt.Fprintln(os.Stderr, e)
		os.Exit(1)
	}
	executable = filepath.Join(dir, "codex-thread-bridge")
	cmd := exec.Command("go", "build", "-o", executable, "../../cmd/codex-thread-bridge")
	if out, e := cmd.CombinedOutput(); e != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", out, e)
		_ = os.RemoveAll(dir)
		os.Exit(1)
	}
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}
func connect(t *testing.T, f *testserver.Fake, home string) *mcp.ClientSession {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	t.Cleanup(cancel)
	command := exec.Command(executable, "--socket", f.Socket)
	command.Dir = t.TempDir()
	if readonly := os.Getenv("CTB_READONLY_HOME"); readonly != "" {
		command.Dir = readonly
	}
	command.Env = append(os.Environ(), "CODEX_HOME="+home, "HOME="+home, "TMPDIR="+home, "XDG_CACHE_HOME="+home, "XDG_STATE_HOME="+home)
	command.Stderr = os.Stderr
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	session, e := client.Connect(ctx, &mcp.CommandTransport{Command: command, TerminateDuration: time.Second}, nil)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session
}
func tool(t *testing.T, s *mcp.ClientSession, name string, args j.Object) j.Object {
	t.Helper()
	r, e := s.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if e != nil {
		t.Fatal(e)
	}
	if r.IsError {
		t.Fatalf("%s: %+v", name, r.Content)
	}
	raw, e := json.Marshal(r.StructuredContent)
	if e != nil {
		t.Fatal(e)
	}
	var out j.Object
	if e = j.Decode(raw, &out); e != nil {
		t.Fatal(e)
	}
	return out
}
func TestStdioInventoryAndWorktree(t *testing.T) {
	f := testserver.Start(t)
	home := filepath.Join(t.TempDir(), "no-state")
	s := connect(t, f, home)
	inventory, e := s.ListTools(context.Background(), nil)
	if e != nil {
		t.Fatal(e)
	}
	raw, _ := json.Marshal(inventory.Tools)
	var got, want []j.Object
	if e = j.Decode(raw, &got); e != nil {
		t.Fatal(e)
	}
	if e = j.Decode(toolData, &want); e != nil {
		t.Fatal(e)
	}
	sort.Slice(got, func(i, k int) bool { return j.String(got[i]["name"]) < j.String(got[k]["name"]) })
	sort.Slice(want, func(i, k int) bool { return j.String(want[i]["name"]) < j.String(want[k]["name"]) })
	// The Go SDK emits the MCP default explicitly. Python omits this hint.
	for _, tool := range want {
		j.Map(tool["annotations"])["idempotentHint"] = false
	}
	if !j.Equal(got, want) {
		t.Fatal("tool schemas differ from the current contract")
	}
	if f.Count("initialize") != 0 {
		t.Fatal("connected before tool use")
	}
	r, e := s.CallTool(context.Background(), &mcp.CallToolParams{Name: "create_thread", Arguments: j.Object{"cwd": t.TempDir(), "environment": j.Object{"type": "worktree"}}})
	if e != nil || !r.IsError {
		t.Fatalf("%v %v", r, e)
	}
	if !strings.Contains(r.Content[0].(*mcp.TextContent).Text, "Worktree support is not available") {
		t.Fatal(r)
	}
	if f.Count("initialize") != 0 {
		t.Fatal("worktree caused RPC")
	}
	if _, e = os.Stat(home); !os.IsNotExist(e) {
		t.Fatal("created state")
	}
}
func TestStdioLifecycleAndRestart(t *testing.T) {
	f := testserver.Start(t)
	dir := t.TempDir()
	home := filepath.Join(dir, "state")
	if readonly := os.Getenv("CTB_READONLY_HOME"); readonly != "" {
		home = readonly
	}
	s := connect(t, f, home)
	created := tool(t, s, "create_thread", j.Object{"cwd": dir, "prompt": "first"})
	id := created["threadId"]
	tool(t, s, "send_message_to_thread", j.Object{"threadId": id, "prompt": "second"})
	one := tool(t, s, "read_thread", j.Object{"threadId": id, "turnLimit": 1})
	two := tool(t, s, "read_thread", j.Object{"threadId": id, "turnLimit": 1, "cursor": one["nextCursor"]})
	text := j.Map(j.List(j.Map(j.List(two["turns"])[0])["messages"])[0])["text"]
	if text != "first" {
		t.Fatal(two)
	}
	for _, tc := range []struct {
		name, key string
		v         any
	}{{"set_thread_title", "title", "Saved"}, {"set_thread_pinned", "pinned", true}, {"set_thread_archived", "archived", true}} {
		r := tool(t, s, tc.name, j.Object{"threadId": id, tc.key: tc.v})
		if r["status"] != "accepted" {
			t.Fatal(r)
		}
	}
	archived := tool(t, s, "list_archived_threads", j.Object{})
	if j.Map(j.List(archived["data"])[0])["name"] != "Saved" {
		t.Fatal(archived)
	}
	tool(t, s, "set_thread_archived", j.Object{"threadId": id, "archived": false})
	tool(t, s, "list_threads", j.Object{})
	f.Edit(func(f *testserver.Fake) { f.Projects = []any{j.Object{"id": "project"}} })
	tool(t, s, "list_projects", j.Object{})
	waited := tool(t, s, "wait_threads", j.Object{"targets": []any{j.Object{"threadId": id}}, "timeoutMs": 0})
	cursor := j.Map(j.List(waited["threads"])[0])["cursor"]
	if e := s.Close(); e != nil {
		t.Fatal(e)
	}
	s = connect(t, f, home)
	again := tool(t, s, "wait_threads", j.Object{"targets": []any{j.Object{"threadId": id, "afterCursor": cursor}}, "timeoutMs": 0})
	row := j.Map(j.List(again["threads"])[0])
	if row["unchanged"] != true {
		t.Fatal(row)
	}
	if _, ok := j.Map(row["progress"])["finalText"]; ok {
		t.Fatal("unchanged final text")
	}
	if f.Count("thread/start") != 1 || f.Count("turn/start") != 2 {
		t.Fatal("replayed operation")
	}
}
func TestCLIAndDiagnostics(t *testing.T) {
	f := testserver.Start(t)
	for _, args := range [][]string{{"--help"}, {"--version"}, {"diagnose", "--socket", f.Socket}, {"--socket", f.Socket, "diagnose"}} {
		cmd := exec.Command(executable, args...)
		cmd.Dir = t.TempDir()
		out, e := cmd.CombinedOutput()
		if e != nil {
			t.Fatalf("%v: %s %v", args, out, e)
		}
		switch args[0] {
		case "--version":
			if strings.TrimSpace(string(out)) != version.Version {
				t.Fatal(string(out))
			}
		case "--help":
			if !strings.Contains(string(out), "diagnose") {
				t.Fatal(string(out))
			}
		default:
			var v j.Object
			if j.Decode(out, &v) != nil || v["stateless"] != true || v["socket"] != f.Socket {
				t.Fatal(string(out))
			}
		}
	}
}
func TestHistoricalLedgerUntouched(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "operations.sqlite3")
	data := []byte("historical data: do not read or change")
	if e := os.WriteFile(path, data, 0600); e != nil {
		t.Fatal(e)
	}
	out, e := exec.Command(executable, "serve", "--state-dir", dir).CombinedOutput()
	if e == nil || !strings.Contains(string(out), "unrecognized arguments") {
		t.Fatalf("%s %v", out, e)
	}
	after, e := os.ReadFile(path)
	if e != nil || string(after) != string(data) {
		t.Fatal("changed ledger")
	}
}
func TestSchemaValidation(t *testing.T) {
	f := testserver.Start(t)
	s := connect(t, f, filepath.Join(t.TempDir(), "absent"))
	for _, tc := range []struct {
		name string
		a    j.Object
	}{{"create_thread", j.Object{"cwd": t.TempDir(), "environment": j.Object{"type": "local", "extra": true}}}, {"wait_threads", j.Object{"targets": []any{j.Object{"threadId": "x", "extra": true}}}}, {"set_thread_pinned", j.Object{"threadId": "x"}}, {"read_thread", j.Object{"threadId": "x", "turnLimit": 1.5}}} {
		r, e := s.CallTool(context.Background(), &mcp.CallToolParams{Name: tc.name, Arguments: tc.a})
		if e != nil || !r.IsError {
			t.Fatalf("accepted %v: %v %v", tc, r, e)
		}
	}
	if f.Count("initialize") != 0 {
		t.Fatal("invalid input caused RPC")
	}
}

func TestStrictCurrentContract(t *testing.T) {
	f := testserver.Start(t)
	s := connect(t, f, filepath.Join(t.TempDir(), "absent"))
	cases := map[string]j.Object{
		"create_thread":          {"cwd": t.TempDir()},
		"send_message_to_thread": {"threadId": "task", "prompt": "hello"},
		"list_projects":          {}, "list_threads": {}, "list_archived_threads": {},
		"read_thread":         {"threadId": "task"},
		"wait_threads":        {"targets": []any{j.Object{"threadId": "task"}}},
		"set_thread_pinned":   {"threadId": "task", "pinned": true},
		"set_thread_archived": {"threadId": "task", "archived": true},
		"set_thread_title":    {"threadId": "task", "title": "Title"},
	}
	for name, args := range cases {
		args["extra"] = "ignored before validation became strict"
		r, err := s.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
		if err != nil || !r.IsError {
			t.Fatalf("%s accepted unknown input: %v", name, err)
		}
	}
	if f.Count("initialize") != 0 {
		t.Fatal("invalid input contacted server")
	}
	var contracts []j.Object
	if err := j.Decode(toolData, &contracts); err != nil {
		t.Fatal(err)
	}
	if len(contracts) != 10 {
		t.Fatal("tool inventory changed")
	}
	for _, tool := range contracts {
		props := j.Map(j.Map(tool["inputSchema"])["properties"])
		switch tool["name"] {
		case "wait_threads":
			if j.Int(j.Map(props["timeoutMs"])["default"]) != 120000 {
				t.Fatal("incorrect default timeout")
			}
		case "send_message_to_thread":
			if props["model"] == nil || props["thinking"] == nil {
				t.Fatal("missing follow-up overrides")
			}
		case "read_thread":
			if j.Map(props["includeOutputs"])["default"] != false {
				t.Fatal("outputs enabled by default")
			}
		}
	}
}

func TestStdioOverridesAndOutputSelection(t *testing.T) {
	f := testserver.Start(t)
	s := connect(t, f, filepath.Join(t.TempDir(), "absent"))
	r := tool(t, s, "create_thread", j.Object{"cwd": t.TempDir(), "model": "other", "thinking": "low"})
	id := r["threadId"]
	r = tool(t, s, "send_message_to_thread", j.Object{"threadId": id, "prompt": "next", "thinking": "high"})
	if !j.Equal(r["settings"], j.Object{"model": "other", "thinking": "high"}) {
		t.Fatal(r)
	}
	f.Edit(func(f *testserver.Fake) {
		turn := j.Map(j.List(f.Threads[j.String(id)]["turns"])[0])
		turn["items"] = append(j.List(turn["items"]), j.Object{"type": "commandExecution", "command": "test", "aggregatedOutput": "result"})
	})
	for _, output := range []any{false, "yes"} {
		r = tool(t, s, "read_thread", j.Object{"threadId": id, "includeOutputs": output})
		turn := j.Map(j.List(r["turns"])[0])
		entry := j.Map(j.List(turn["tools"])[0])
		if (entry["output"] != nil) != (output == "yes") {
			t.Fatal(r)
		}
	}
}

//go:embed testdata/python_validation.json
var validationFixtures []byte

func TestPythonScalarValidation(t *testing.T) {
	var cases []j.Object
	if e := j.Decode(validationFixtures, &cases); e != nil {
		t.Fatal(e)
	}
	var tools []j.Object
	if e := j.Decode(referenceTools, &tools); e != nil {
		t.Fatal(e)
	}
	for _, tc := range cases {
		t.Run(j.String(tc["tool"])+"/"+j.PythonJSON(tc["input"]), func(t *testing.T) {
			args := j.Map(tc["input"])
			normalizeInput(args)
			var schema jsonschema.Schema
			for _, tool := range tools {
				if tool["name"] == tc["tool"] {
					raw, _ := json.Marshal(tool["inputSchema"])
					if e := json.Unmarshal(raw, &schema); e != nil {
						t.Fatal(e)
					}
				}
			}
			resolved, e := schema.Resolve(nil)
			if e != nil {
				t.Fatal(e)
			}
			raw, _ := json.Marshal(args)
			var validation any
			_ = json.Unmarshal(raw, &validation)
			e = resolved.Validate(validation)
			if (e == nil) != (tc["accepted"] == true) {
				t.Fatalf("validation changed: %v", e)
			}
			if e == nil {
				for k, want := range j.Map(tc["output"]) {
					if v, ok := args[k]; ok && !j.Equal(v, want) {
						t.Fatalf("conversion of %s changed: %v != %v", k, v, want)
					}
				}
			}
		})
	}
}
