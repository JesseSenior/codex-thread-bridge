package bridge

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	j "github.com/JesseSenior/codex-thread-bridge/internal/jsonutil"
	"github.com/JesseSenior/codex-thread-bridge/internal/rpc"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// resolvedPath also resolves an existing parent when the final socket is absent.
func resolvedPath(path string) string {
	absolute, _ := filepath.Abs(path)
	if p, err := filepath.EvalSymlinks(absolute); err == nil {
		return p
	}
	parent := filepath.Dir(absolute)
	if parent == absolute {
		return absolute
	}
	return filepath.Join(resolvedPath(parent), filepath.Base(absolute))
}
func (b *Bridge) scope(id string) j.Object {
	return j.Object{"v": 2, "host": resolvedPath(b.RPC.Socket), "threadId": id}
}
func (b *Bridge) decodeCursor(cursor, id string) (j.Object, error) {
	fail := errors.New("Invalid wait cursor: require version 2 for this host and task")
	raw, err := base64.URLEncoding.DecodeString(cursor)
	if err != nil {
		return nil, fail
	}
	var v j.Object
	if j.Decode(raw, &v) != nil || len(v) != 6 {
		return nil, fail
	}
	for k, want := range b.scope(id) {
		if !j.Equal(v[k], want) {
			return nil, fail
		}
	}
	for _, key := range []string{"digest", "event", "final"} {
		value, ok := v[key].(string)
		if !ok {
			return nil, fail
		}
		if key == "final" && value == "" {
			continue
		}
		decoded, err := hex.DecodeString(value)
		if err != nil || len(decoded) != sha256.Size {
			return nil, fail
		}
	}
	return v, nil
}
func digest(v any) string { return fmt.Sprintf("%x", sha256.Sum256([]byte(j.PythonJSON(v)))) }
func (b *Bridge) snapshot(ctx context.Context, id string) (j.Object, j.Object, error) {
	read, err := b.RPC.Call(ctx, "thread/read", j.Object{"threadId": id})
	if err != nil {
		return nil, nil, err
	}
	thread := j.Map(read["thread"])
	turn, err := b.latest(ctx, id, "full")
	if err != nil {
		return nil, nil, err
	}
	status := j.Map(thread["status"])
	flags := j.List(status["activeFlags"])
	methods := map[string]bool{}
	for _, req := range b.RPC.Interactions() {
		if j.Map(req["params"])["threadId"] == id {
			methods[j.String(req["method"])] = true
		}
	}
	pending := make([]string, 0, len(methods))
	for method := range methods {
		pending = append(pending, method)
	}
	sort.Strings(pending)
	interaction := len(pending) > 0
	for _, flag := range flags {
		interaction = interaction || flag == "waitingOnApproval" || flag == "waitingOnUserInput"
	}
	outcome := "running"
	ts := j.Map(turn)["status"]
	switch {
	case interaction:
		outcome = "interaction_required"
	case status["type"] == "active":
	case ts == "failed" || ts == "interrupted":
		outcome = j.String(ts)
	case status["type"] == "systemError":
		outcome = "failed"
	case turn == nil || ts == "completed":
		outcome = "completed"
	}
	p := make([]any, len(pending))
	for i, v := range pending {
		p[i] = v
	}
	progress, finalText := progress(j.Map(turn))
	snapshot := j.Object{"threadId": id, "outcome": outcome, "status": status, "progress": progress, "pendingMethods": p}
	scope := b.scope(id)
	scope["digest"] = digest(snapshot)
	interactionFlags := []string{}
	for _, flag := range flags {
		if flag == "waitingOnApproval" || flag == "waitingOnUserInput" {
			interactionFlags = append(interactionFlags, j.String(flag))
		}
	}
	sort.Strings(interactionFlags)
	scope["event"] = digest(j.Object{"outcome": outcome, "turnId": progress["turnId"], "turnStatus": ts, "error": progress["error"], "pendingMethods": p, "flags": interactionFlags})
	scope["final"] = ""
	if finalText != "" {
		scope["final"] = digest(j.Object{"turnId": progress["turnId"], "text": finalText})
	}
	return snapshot, scope, nil
}
func (b *Bridge) Wait(ctx context.Context, targets []any, timeout int) (j.Object, error) {
	if len(targets) < 1 || len(targets) > 8 {
		return nil, errors.New("targets must contain 1 to 8 tasks")
	}
	if timeout < 0 || timeout > 120000 {
		return nil, errors.New("timeoutMs must be between 0 and 120000")
	}
	cursors := map[string]j.Object{}
	ids := make([]string, len(targets))
	seen := map[string]bool{}
	for i, v := range targets {
		t := j.Map(v)
		id := j.String(t["threadId"])
		if err := nonempty(id, "threadId", 128); err != nil {
			return nil, err
		}
		if seen[id] {
			return nil, errors.New("targets must contain distinct task IDs")
		}
		seen[id] = true
		ids[i] = id
		if cursor := j.String(t["afterCursor"]); cursor != "" {
			digest, err := b.decodeCursor(cursor, id)
			if err != nil {
				return nil, err
			}
			cursors[id] = digest
		}
	}
	end := time.Now().Add(time.Duration(timeout) * time.Millisecond)
	for {
		type item struct {
			s j.Object
			d j.Object
			e error
		}
		results := make([]item, len(ids))
		var wg sync.WaitGroup
		for i, id := range ids {
			wg.Add(1)
			go func() { defer wg.Done(); results[i].s, results[i].d, results[i].e = b.snapshot(ctx, id) }()
		}
		wg.Wait()
		snapshots, errs := []any{}, []any{}
		ready := false
		for i, r := range results {
			if r.e != nil {
				e := j.Object{"threadId": ids[i], "error": r.e.Error()}
				var api *rpc.Error
				if errors.As(r.e, &api) {
					e["rpcError"] = api.Data
				}
				errs = append(errs, e)
				continue
			}
			previous := cursors[ids[i]]
			finalChanged := r.d["final"] != "" && !j.Equal(previous["final"], r.d["final"])
			unchanged := j.Equal(previous["digest"], r.d["digest"]) && !finalChanged
			if !finalChanged {
				delete(j.Map(r.s["progress"]), "finalText")
			}
			if r.d["final"] == "" && previous["final"] != nil {
				r.d["final"] = previous["final"]
			}
			r.s["cursor"] = base64.URLEncoding.EncodeToString([]byte(j.PythonJSON(r.d)))
			r.s["unchanged"] = unchanged
			snapshots = append(snapshots, r.s)
			ready = ready || (r.s["outcome"] != "running" && (!j.Equal(previous["event"], r.d["event"]) || finalChanged))
		}
		remaining := time.Until(end)
		if ready || len(errs) > 0 || remaining <= 0 {
			return j.Object{"threads": snapshots, "errors": errs, "timedOut": !ready && len(errs) == 0 && timeout > 0}, nil
		}
		if remaining > 250*time.Millisecond {
			remaining = 250 * time.Millisecond
		}
		timer := time.NewTimer(remaining)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}
