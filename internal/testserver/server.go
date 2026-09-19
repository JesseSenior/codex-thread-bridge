// Package testserver provides an isolated App Server for protocol tests.
package testserver

import (
	"context"
	"encoding/json"
	"fmt"
	j "github.com/JesseSenior/codex-thread-bridge/internal/jsonutil"
	"github.com/JesseSenior/codex-thread-bridge/internal/rpc"
	"github.com/coder/websocket"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

type Call struct {
	Method string
	Params j.Object
}
type Fake struct {
	mu            sync.Mutex
	Socket        string
	Calls         []Call
	Threads       map[string]j.Object
	Order         []string
	Reject        map[string]j.Object
	Models        []any
	Config        j.Object
	ModelPageSize int
	Projects      []any
	Complete      bool
	Drop          string
	Pause         string
	Paused        chan struct{}
	Release       chan struct{}
	Before        func(string, j.Object)
	Override      j.Object
	Extensions    []string
	PendingMethod string
	Resolution    string
	Responses     []j.Object
	conns         []*websocket.Conn
}

func Start(t *testing.T) *Fake {
	t.Helper()
	dir, err := os.MkdirTemp("", "ctb-")
	if err != nil {
		t.Fatal(err)
	}
	f := &Fake{Socket: filepath.Join(dir, "app.sock"), Threads: map[string]j.Object{}, Reject: map[string]j.Object{}, Complete: true, Override: j.Object{}, Projects: []any{}, Paused: make(chan struct{}, 1), Release: make(chan struct{})}
	f.Config = j.Object{"model": "server-default", "model_reasoning_effort": "medium"}
	for _, name := range []string{"server-default", "other", "saved-model"} {
		f.Models = append(f.Models, j.Object{"id": name, "model": name, "isDefault": name == "server-default", "defaultReasoningEffort": "medium", "supportedReasoningEfforts": []any{j.Object{"reasoningEffort": "low"}, j.Object{"reasoningEffort": "medium"}, j.Object{"reasoningEffort": "high"}}})
	}
	listener, err := net.Listen("unix", f.Socket)
	if err != nil {
		t.Fatal(err)
	}
	s := &http.Server{Handler: http.HandlerFunc(f.handle), ReadHeaderTimeout: time.Second}
	go func() { _ = s.Serve(listener) }()
	t.Cleanup(func() {
		f.mu.Lock()
		for _, c := range f.conns {
			_ = c.CloseNow()
		}
		f.mu.Unlock()
		_ = s.Close()
		_ = os.RemoveAll(dir)
	})
	return f
}
func (f *Fake) Edit(fn func(*Fake)) { f.mu.Lock(); defer f.mu.Unlock(); fn(f) }
func (f *Fake) Count(method string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.Calls {
		if c.Method == method {
			n++
		}
	}
	return n
}
func (f *Fake) Params(method string) j.Object {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.Calls {
		if c.Method == method {
			return clone(c.Params)
		}
	}
	return nil
}
func (f *Fake) Thread(id string) j.Object {
	f.mu.Lock()
	defer f.mu.Unlock()
	return clone(f.Threads[id])
}
func (f *Fake) Add(cwd string, fields j.Object) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.add(cwd, fields)
}
func (f *Fake) add(cwd string, fields j.Object) string {
	id := fmt.Sprintf("thread-%d", len(f.Threads)+1)
	thread := j.Object{"id": id, "cwd": cwd, "status": j.Object{"type": "idle"}, "turns": []any{}, "archived": false}
	for k, v := range fields {
		thread[k] = v
	}
	f.Threads[id] = thread
	f.Order = append(f.Order, id)
	return id
}
func clone(m j.Object) j.Object {
	b, _ := json.Marshal(m)
	var out j.Object
	_ = j.Decode(b, &out)
	return out
}
func page(rows []any, p j.Object) j.Object {
	start := 0
	if cursor := j.String(p["cursor"]); cursor != "" {
		var c j.Object
		_ = j.Decode([]byte(cursor), &c)
		start = j.Int(c["offset"])
	}
	end := start + j.Int(p["limit"])
	var cursor any
	if end < len(rows) {
		cursor = j.PythonJSON(j.Object{"offset": end, "scope": j.Object{"kind": "opaque"}})
	} else {
		end = len(rows)
	}
	if start > len(rows) {
		start = len(rows)
	}
	return clone(j.Object{"data": rows[start:end], "nextCursor": cursor})
}
func Sandbox(p j.Object) j.Object {
	kind := j.Default(p, "sandbox", "workspace-write")
	if p["permissions"] == ":read-only" {
		kind = "read-only"
	}
	if kind == "read-only" {
		return j.Object{"type": "readOnly", "networkAccess": false}
	}
	if kind == "danger-full-access" {
		return j.Object{"type": "dangerFullAccess"}
	}
	config := j.Map(j.Map(p["config"])["sandbox_workspace_write"])
	return j.Object{"type": "workspaceWrite", "writableRoots": j.Default(config, "writable_roots", []any{}), "networkAccess": j.Default(config, "network_access", false), "excludeTmpdirEnvVar": j.Default(config, "exclude_tmpdir_env_var", false), "excludeSlashTmp": j.Default(config, "exclude_slash_tmp", false)}
}
func (f *Fake) dispatch(method string, p j.Object) (j.Object, *rpc.Error) {
	if method == "initialize" {
		return j.Object{"userAgent": "fake/0.154.0"}, nil
	}
	if method == "config/read" {
		return j.Object{"config": clone(f.Config)}, nil
	}
	if method == "model/list" {
		params := clone(p)
		if f.ModelPageSize > 0 {
			params["limit"] = f.ModelPageSize
		}
		return page(f.Models, params), nil
	}
	if method == "thread/start" {
		id := f.add(j.String(p["cwd"]), nil)
		out := j.Object{"thread": clone(f.Threads[id]), "cwd": p["cwd"], "model": j.Default(p, "model", "server-default"), "reasoningEffort": j.Default(j.Map(p["config"]), "model_reasoning_effort", "medium"), "sandbox": Sandbox(p), "approvalPolicy": j.Default(p, "approvalPolicy", "on-request")}
		if v, ok := p["permissions"]; ok {
			out["activePermissionProfile"] = j.Object{"id": v}
		}
		f.Threads[id]["model"], f.Threads[id]["reasoningEffort"] = out["model"], out["reasoningEffort"]
		return out, nil
	}
	if method == "project/list" {
		return page(f.Projects, p), nil
	}
	if method == "thread/list" {
		rows := []any{}
		for _, id := range f.Order {
			thread := f.Threads[id]
			if j.Equal(thread["archived"], j.Default(p, "archived", false)) && (p["cwd"] == nil || j.Equal(p["cwd"], thread["cwd"])) {
				row := clone(thread)
				row["turns"] = []any{}
				rows = append(rows, row)
			}
		}
		return page(rows, p), nil
	}
	if method == "thread/goal/get" {
		return j.Object{"goal": nil}, nil
	}
	id := j.String(p["threadId"])
	thread := f.Threads[id]
	if thread == nil {
		return nil, &rpc.Error{Method: method, Data: j.Object{"code": -32602, "message": "task does not exist"}}
	}
	switch method {
	case "thread/settings/update":
		if p["model"] != nil {
			thread["model"] = p["model"]
		}
		if p["effort"] != nil {
			thread["reasoningEffort"] = p["effort"]
		}
		return j.Object{}, nil
	case "thread/read":
		row := clone(thread)
		row["turns"] = []any{}
		return j.Object{"thread": row}, nil
	case "thread/turns/list":
		turns := j.List(thread["turns"])
		rows := make([]any, len(turns))
		for i, v := range turns {
			rows[len(turns)-i-1] = v
		}
		return page(rows, p), nil
	case "thread/resume":
		thread["status"] = j.Object{"type": "idle"}
		thread["model"], thread["reasoningEffort"] = p["model"], j.Map(p["config"])["model_reasoning_effort"]
		out := j.Object{"thread": clone(thread), "cwd": thread["cwd"], "sandbox": Sandbox(p), "approvalPolicy": p["approvalPolicy"], "model": p["model"], "reasoningEffort": j.Map(p["config"])["model_reasoning_effort"], "runtimeWorkspaceRoots": j.Default(p, "runtimeWorkspaceRoots", []any{}), "approvalsReviewer": p["approvalsReviewer"], "activePermissionProfile": nil}
		if p["permissions"] != nil {
			out["activePermissionProfile"] = j.Object{"id": p["permissions"]}
		}
		for k, v := range f.Override {
			out[k] = v
		}
		return out, nil
	case "turn/start":
		if j.Map(thread["status"])["type"] == "active" {
			return nil, &rpc.Error{Method: method, Data: j.Object{"code": -32600, "message": "task already active"}}
		}
		turns := j.List(thread["turns"])
		status, state := "completed", "idle"
		if !f.Complete {
			status, state = "inProgress", "active"
		}
		turn := j.Object{"id": fmt.Sprintf("turn-%d", len(turns)+1), "status": status, "model": thread["model"], "effort": thread["reasoningEffort"], "items": []any{j.Object{"type": "agentMessage", "text": j.Map(j.List(p["input"])[0])["text"]}}}
		thread["turns"] = append(turns, turn)
		thread["status"] = j.Object{"type": state}
		return j.Object{"turn": clone(turn)}, nil
	case "turn/steer":
		turns := j.List(thread["turns"])
		if j.Map(thread["status"])["type"] != "active" || len(turns) == 0 || !j.Equal(j.Map(turns[len(turns)-1])["id"], p["expectedTurnId"]) {
			return nil, &rpc.Error{Method: method, Data: j.Object{"code": -32600, "message": "turn no longer active"}}
		}
		return j.Object{"turnId": p["expectedTurnId"]}, nil
	case "thread/name/set":
		thread["name"] = p["name"]
	case "thread/section/move":
		thread["section"] = p["sectionId"]
	case "thread/archive", "thread/unarchive":
		thread["archived"] = method == "thread/archive"
	default:
		return nil, &rpc.Error{Method: method, Data: j.Object{"code": -32601, "message": "unsupported"}}
	}
	return j.Object{}, nil
}
func (f *Fake) handle(w http.ResponseWriter, r *http.Request) {
	ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
	if err != nil {
		return
	}
	defer ws.CloseNow()
	ws.SetReadLimit(16 * 1024 * 1024)
	f.mu.Lock()
	f.Extensions = append(f.Extensions, r.Header.Get("Sec-WebSocket-Extensions"))
	f.conns = append(f.conns, ws)
	f.mu.Unlock()
	send := func(v any) error {
		b, _ := json.Marshal(v)
		return ws.Write(context.Background(), websocket.MessageText, b)
	}
	for {
		_, raw, err := ws.Read(context.Background())
		if err != nil {
			return
		}
		var msg j.Object
		if j.Decode(raw, &msg) != nil {
			return
		}
		method := j.String(msg["method"])
		p := j.Map(msg["params"])
		f.mu.Lock()
		if method == "" {
			f.Responses = append(f.Responses, msg)
			f.mu.Unlock()
			continue
		}
		f.Calls = append(f.Calls, Call{method, p})
		if method == "initialized" {
			f.mu.Unlock()
			continue
		}
		if f.Before != nil {
			f.Before(method, p)
		}
		var result j.Object
		var api *rpc.Error
		if reject := f.Reject[method]; reject != nil {
			api = &rpc.Error{Method: method, Data: reject}
		} else {
			result, api = f.dispatch(method, p)
		}
		drop, pause, pending, resolution := f.Drop == method, f.Pause == method, f.PendingMethod, f.Resolution
		f.mu.Unlock()
		if api != nil {
			if send(j.Object{"id": msg["id"], "error": api.Data}) != nil {
				return
			}
			continue
		}
		if pause {
			select {
			case f.Paused <- struct{}{}:
			default:
			}
			select {
			case <-f.Release:
			case <-r.Context().Done():
				return
			}
		}
		if pending != "" && method == "thread/resume" {
			_ = send(j.Object{"id": "pending-request", "method": pending, "params": j.Object{"threadId": "thread-1", "turnId": "turn-1"}})
		}
		if resolution != "" && method == "thread/read" {
			params := j.Object{"threadId": "thread-1", "requestId": "pending-request"}
			if resolution == "turn/completed" {
				params = j.Object{"threadId": "thread-1", "turn": j.Object{"id": "turn-1"}}
			}
			_ = send(j.Object{"method": resolution, "params": params})
		}
		_ = send(j.Object{"method": "thread/status/changed", "params": j.Object{}})
		if drop {
			_ = ws.Close(websocket.StatusNormalClosure, "")
			return
		}
		if send(j.Object{"id": msg["id"], "result": result}) != nil {
			return
		}
	}
}
