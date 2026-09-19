// Package bridge implements stateless task operations on one App Server.
package bridge

import (
	"context"
	"errors"
	"fmt"
	j "github.com/JesseSenior/codex-thread-bridge/internal/jsonutil"
	"github.com/JesseSenior/codex-thread-bridge/internal/rpc"
	"github.com/JesseSenior/codex-thread-bridge/internal/settings"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"unicode/utf8"
)

const PinnedSectionID = "01984de2-8f74-7c91-a3b2-5c5e937cf318"

type Bridge struct {
	RPC      *rpc.Client
	mutation sync.Mutex
}

func New(client *rpc.Client) *Bridge { return &Bridge{RPC: client} }
func nonempty(v any, name string, max int) error {
	s, ok := v.(string)
	if !ok || strings.TrimSpace(s) == "" || utf8.RuneCountInString(s) > max {
		return fmt.Errorf("%s must contain 1–%d characters", name, max)
	}
	return nil
}
func pageSize(limit int) error {
	if limit < 1 || limit > 100 {
		return errors.New("limit must be between 1 and 100")
	}
	return nil
}
func Clip(v any, limit int, display bool) any {
	switch x := v.(type) {
	case string:
		if display && utf8.RuneCountInString(x) > limit {
			return string([]rune(x)[:limit]) + fmt.Sprintf("\n[truncated; original length %d characters]", utf8.RuneCountInString(x))
		}
	case []any:
		out := make([]any, len(x))
		for i, v := range x {
			out[i] = Clip(v, limit, display)
		}
		return out
	case map[string]any:
		out := j.Object{}
		for k, v := range x {
			d := k == "text" || k == "preview" || k == "summary" || k == "objective" || k == "aggregatedOutput"
			out[k] = Clip(v, limit, d)
		}
		return out
	}
	return v
}
func (b *Bridge) Diagnostics(ctx context.Context) (j.Object, error) {
	if err := b.RPC.Connect(ctx); err != nil {
		return nil, err
	}
	return j.Object{"server": b.RPC.Info(), "socket": b.RPC.Socket, "stateless": true}, nil
}

type callFunc func(string, j.Object) (j.Object, error)

func (b *Bridge) mutate(ctx context.Context, known j.Object, action func(callFunc, j.Object) error) (j.Object, error) {
	result := j.Object{"status": "accepted"}
	for k, v := range known {
		result[k] = v
	}
	b.mutation.Lock()
	defer b.mutation.Unlock()
	call := func(method string, p j.Object) (j.Object, error) {
		result["method"] = method
		return b.RPC.Call(ctx, method, p)
	}
	err := action(call, result)
	if err == nil {
		return result, nil
	}
	var api *rpc.Error
	var transport *rpc.TransportError
	var setting *settings.Error
	switch {
	case errors.As(err, &api):
		result["status"] = "failed"
		result["error"] = err.Error()
		result["rpcError"] = api.Data
	case errors.As(err, &transport):
		result["status"] = "failed"
		if transport.MayHaveBeenSent {
			result["status"] = "outcome_unknown"
		}
		result["error"] = err.Error()
		result["recovery"] = "Do not resend automatically. Inspect the task and its history."
	case errors.As(err, &setting):
		result["status"] = "failed"
		result["error"] = err.Error()
		result["messageSent"] = false
	default:
		return nil, err
	}
	return result, nil
}
func (b *Bridge) Create(ctx context.Context, a j.Object) (j.Object, error) {
	if env := a["environment"]; env != nil {
		e := j.Map(env)
		if e["type"] == "worktree" {
			return nil, errors.New("Worktree support is not available. Use an existing directory.")
		}
		if !j.Equal(e, j.Object{"type": "local"}) {
			return nil, errors.New("Only environment {type: local} is supported")
		}
	}
	directory := j.String(a["cwd"])
	info, err := os.Stat(directory)
	if !filepath.IsAbs(directory) || err != nil || !info.IsDir() {
		return nil, errors.New("cwd must be an existing absolute directory on this remote host")
	}
	directory, err = filepath.EvalSymlinks(directory)
	if err != nil {
		return nil, err
	}
	for _, key := range []string{"prompt", "title", "model"} {
		if a[key] != nil {
			if err := nonempty(a[key], key, 100000); err != nil {
				return nil, err
			}
		}
	}
	if a["sandbox"] != nil && a["permissions"] != nil {
		return nil, errors.New("sandbox and permissions cannot be combined")
	}
	params := j.Object{"cwd": directory, "ephemeral": false}
	for _, k := range []string{"model", "sandbox", "approvalPolicy", "permissions"} {
		if a[k] != nil {
			params[k] = a[k]
		}
	}
	if a["thinking"] != nil {
		params["config"] = j.Object{"model_reasoning_effort": a["thinking"]}
	}
	return b.mutate(ctx, nil, func(call callFunc, result j.Object) error {
		creation, err := call("thread/start", params)
		if err != nil {
			return err
		}
		tid := j.Map(creation["thread"])["id"]
		result["threadId"] = tid
		result["creation"] = creation
		for _, key := range []string{"cwd", "model", "approvalPolicy"} {
			if v, ok := params[key]; ok && !j.Equal(creation[key], v) {
				return settings.Fail("Created settings differ from the request; no prompt was sent")
			}
		}
		if a["sandbox"] != nil {
			kind := map[string]string{"read-only": "readOnly", "workspace-write": "workspaceWrite", "danger-full-access": "dangerFullAccess"}[j.String(a["sandbox"])]
			if kind == "" || j.Map(creation["sandbox"])["type"] != kind {
				return settings.Fail("Created sandbox differs; no prompt was sent")
			}
		}
		if a["permissions"] != nil && !j.Equal(j.Map(creation["activePermissionProfile"])["id"], a["permissions"]) {
			return settings.Fail("Created permission profile differs; no prompt was sent")
		}
		if a["thinking"] != nil && !j.Equal(creation["reasoningEffort"], a["thinking"]) {
			return settings.Fail("Created reasoning effort differs; no prompt was sent")
		}
		if a["title"] != nil {
			if _, err = call("thread/name/set", j.Object{"threadId": tid, "name": a["title"]}); err != nil {
				return err
			}
		}
		if a["prompt"] != nil {
			turn, err := call("turn/start", j.Object{"threadId": tid, "input": input(a["prompt"])})
			if err != nil {
				return err
			}
			result["turnId"] = j.Map(turn["turn"])["id"]
		}
		return nil
	})
}
func input(prompt any) []any { return []any{j.Object{"type": "text", "text": prompt}} }
func (b *Bridge) latest(ctx context.Context, id, items string) (any, error) {
	p, err := b.RPC.Call(ctx, "thread/turns/list", j.Object{"threadId": id, "limit": 1, "itemsView": items})
	if err != nil {
		return nil, err
	}
	rows := j.List(p["data"])
	if len(rows) == 0 {
		return nil, nil
	}
	return rows[0], nil
}
func (b *Bridge) Send(ctx context.Context, id, prompt string) (j.Object, error) {
	if err := nonempty(id, "threadId", 128); err != nil {
		return nil, err
	}
	if err := nonempty(prompt, "prompt", 100000); err != nil {
		return nil, err
	}
	return b.mutate(ctx, j.Object{"threadId": id}, func(call callFunc, result j.Object) error {
		read, err := call("thread/read", j.Object{"threadId": id})
		if err != nil {
			return err
		}
		thread := j.Map(read["thread"])
		if j.Map(thread["status"])["type"] == "notLoaded" {
			params, sandbox, err := settings.Saved(thread)
			if err != nil {
				return err
			}
			resumed, err := call("thread/resume", params)
			if err != nil {
				return err
			}
			saved := j.Object{}
			for k, v := range resumed {
				if k != "thread" {
					saved[k] = v
				}
			}
			result["settings"] = saved
			if err = settings.Verify(resumed, params, sandbox); err != nil {
				return err
			}
			thread = j.Map(resumed["thread"])
		}
		if j.Map(thread["status"])["type"] == "active" {
			v, err := b.latest(ctx, id, "summary")
			if err != nil {
				return err
			}
			turn := j.Map(v)
			if turn == nil || turn["status"] != "inProgress" {
				return settings.Fail("Task changed while reading it; message was not sent")
			}
			result["turnId"] = turn["id"]
			steered, err := call("turn/steer", j.Object{"threadId": id, "expectedTurnId": turn["id"], "input": input(prompt)})
			if err != nil {
				return err
			}
			result["turnId"] = steered["turnId"]
			result["delivery"] = "steered"
		} else {
			started, err := call("turn/start", j.Object{"threadId": id, "input": input(prompt)})
			if err != nil {
				return err
			}
			result["turnId"] = j.Map(started["turn"])["id"]
			result["delivery"] = "started"
		}
		return nil
	})
}
func (b *Bridge) Projects(ctx context.Context, limit int, cursor string) (j.Object, error) {
	if err := pageSize(limit); err != nil {
		return nil, err
	}
	p := j.Object{"limit": limit}
	if cursor != "" {
		p["cursor"] = cursor
	}
	page, err := b.RPC.Call(ctx, "project/list", p)
	if err != nil {
		return nil, err
	}
	if len(j.List(page["data"])) == 0 && cursor == "" {
		return nil, errors.New("Desktop required: this App Server exposes no project records. Use Desktop to inspect its saved project catalog, or create_thread with an explicit cwd. An empty catalog does not establish whether Desktop is connected.")
	}
	return page, nil
}
func (b *Bridge) Threads(ctx context.Context, limit int, cursor string, cwd any, archived bool) (j.Object, error) {
	if err := pageSize(limit); err != nil {
		return nil, err
	}
	p := j.Object{"limit": limit, "archived": archived, "useStateDbOnly": true}
	if cursor != "" {
		p["cursor"] = cursor
	}
	if cwd != nil {
		p["cwd"] = cwd
	}
	v, err := b.RPC.Call(ctx, "thread/list", p)
	if err != nil {
		return nil, err
	}
	return j.Map(Clip(v, 4000, false)), nil
}
func (b *Bridge) Read(ctx context.Context, id string, limit int, cursor string, chars int) (j.Object, error) {
	if err := nonempty(id, "threadId", 128); err != nil {
		return nil, err
	}
	if err := pageSize(limit); err != nil {
		return nil, err
	}
	if chars < 100 || chars > 20000 {
		return nil, errors.New("maxOutputCharsPerItem must be between 100 and 20000")
	}
	read, err := b.RPC.Call(ctx, "thread/read", j.Object{"threadId": id})
	if err != nil {
		return nil, err
	}
	p := j.Object{"threadId": id, "limit": limit, "itemsView": "full"}
	if cursor != "" {
		p["cursor"] = cursor
	}
	page, err := b.RPC.Call(ctx, "thread/turns/list", p)
	if err != nil {
		return nil, err
	}
	return j.Map(Clip(j.Object{"thread": read["thread"], "turnsPage": page}, chars, false)), nil
}
func (b *Bridge) Metadata(ctx context.Context, operation, id string, value any) (j.Object, error) {
	if err := nonempty(id, "threadId", 128); err != nil {
		return nil, err
	}
	if operation == "title" {
		if err := nonempty(value, "title", 500); err != nil {
			return nil, err
		}
	}
	return b.mutate(ctx, j.Object{"threadId": id}, func(call callFunc, result j.Object) error {
		var err error
		switch operation {
		case "title":
			_, err = call("thread/name/set", j.Object{"threadId": id, "name": value})
		case "pinned":
			var section any
			if value == true {
				section = PinnedSectionID
			}
			_, err = call("thread/section/move", j.Object{"threadId": id, "sectionId": section})
		case "archived":
			method := "thread/unarchive"
			if value == true {
				read, e := call("thread/read", j.Object{"threadId": id})
				if e != nil {
					return e
				}
				if j.Map(j.Map(read["thread"])["status"])["type"] == "active" {
					return settings.Fail("Cannot archive a running task. Wait for it to finish.")
				}
				method = "thread/archive"
			}
			_, err = call(method, j.Object{"threadId": id})
		default:
			return fmt.Errorf("unknown metadata operation %s", operation)
		}
		if err == nil {
			result[operation] = value
		}
		return err
	})
}
