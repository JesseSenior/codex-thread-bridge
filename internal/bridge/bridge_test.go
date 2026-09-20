package bridge

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	j "github.com/JesseSenior/codex-thread-bridge/internal/jsonutil"
	"github.com/JesseSenior/codex-thread-bridge/internal/rpc"
	"github.com/JesseSenior/codex-thread-bridge/internal/testserver"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var ctx = context.Background()

func setup(t *testing.T) (*Bridge, *testserver.Fake, string) {
	t.Helper()
	f := testserver.Start(t)
	c := rpc.New(f.Socket)
	c.Timeout = 500 * time.Millisecond
	t.Cleanup(c.Close)
	return New(c), f, t.TempDir()
}
func must(t *testing.T, v j.Object, err error) j.Object {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
	return v
}
func create(t *testing.T, b *Bridge, a j.Object) j.Object {
	t.Helper()
	v, e := b.Create(ctx, a, "")
	return must(t, v, e)
}
func send(t *testing.T, b *Bridge, id, prompt string) j.Object {
	t.Helper()
	v, e := b.Send(ctx, id, prompt, nil, nil, "")
	return must(t, v, e)
}
func wait(t *testing.T, b *Bridge, targets []any, ms int) j.Object {
	t.Helper()
	v, e := b.Wait(ctx, targets, ms)
	return must(t, v, e)
}
func eq(t *testing.T, a, b any) {
	t.Helper()
	if !j.Equal(a, b) {
		t.Fatalf("got %s; want %s", j.PythonJSON(a), j.PythonJSON(b))
	}
}
func first(v j.Object) j.Object { return j.Map(j.List(v["threads"])[0]) }
func TestDefaultsAndExactPrompt(t *testing.T) {
	b, f, dir := setup(t)
	r := create(t, b, j.Object{"cwd": dir, "prompt": "  exact\nmessage  ", "title": "Example"})
	eq(t, r["status"], "accepted")
	eq(t, j.Map(r["creation"])["model"], "server-default")
	resolved, _ := filepath.EvalSymlinks(dir)
	eq(t, f.Params("thread/start"), j.Object{"cwd": resolved, "ephemeral": false})
	id := j.String(r["threadId"])
	thread := f.Thread(id)
	eq(t, j.Map(j.List(j.Map(j.List(thread["turns"])[0])["items"])[1])["text"], "  exact\nmessage  ")
	eq(t, thread["name"], "Example")
	next := send(t, b, id, "next")
	eq(t, next["delivery"], "started")
	eq(t, next["turnId"], "turn-2")
	eq(t, f.Count("thread/resume"), 0)
	eq(t, len(f.Params("turn/start")), 2)
}
func TestEmptyCreationAndOverrides(t *testing.T) {
	b, f, dir := setup(t)
	r := create(t, b, j.Object{"cwd": dir, "model": "other", "thinking": "low", "sandbox": "read-only", "approvalPolicy": "untrusted"})
	eq(t, r["status"], "accepted")
	eq(t, r["turnId"], nil)
	eq(t, f.Count("turn/start"), 0)
	eq(t, j.Map(r["creation"])["approvalPolicy"], "untrusted")
	eq(t, j.Map(r["creation"])["reasoningEffort"], "low")
}
func TestSteerAndCompletionRace(t *testing.T) {
	b, f, dir := setup(t)
	f.Edit(func(f *testserver.Fake) { f.Complete = false })
	r := create(t, b, j.Object{"cwd": dir, "prompt": "start"})
	id := j.String(r["threadId"])
	eq(t, send(t, b, id, "steer")["delivery"], "steered")
	f.Edit(func(f *testserver.Fake) {
		f.Before = func(method string, _ j.Object) {
			if method == "turn/steer" {
				f.Threads[id]["status"] = j.Object{"type": "idle"}
				j.Map(j.List(f.Threads[id]["turns"])[0])["status"] = "completed"
			}
		}
	})
	r = send(t, b, id, "race")
	eq(t, r["status"], "failed")
	eq(t, j.Map(r["rpcError"])["code"], -32600)
	eq(t, f.Count("turn/start"), 1)
	eq(t, f.Count("turn/steer"), 2)
}
func TestIdleStartRace(t *testing.T) {
	b, f, dir := setup(t)
	id := f.Add(dir, nil)
	f.Edit(func(f *testserver.Fake) {
		f.Before = func(method string, _ j.Object) {
			if method == "turn/start" {
				f.Threads[id]["status"] = j.Object{"type": "active"}
			}
		}
	})
	eq(t, send(t, b, id, "race")["status"], "failed")
	eq(t, f.Count("turn/start"), 1)
	eq(t, f.Count("turn/steer"), 0)
}

func TestSteeringMessageIdentity(t *testing.T) {
	b, f, dir := setup(t)
	f.Edit(func(f *testserver.Fake) { f.Complete = false })
	id := j.String(create(t, b, j.Object{"cwd": dir, "prompt": "start"})["threadId"])
	seen := map[string]bool{}
	for _, source := range []string{"", "source-peer", "source-peer"} {
		prompt := "  Follow-up <&>\n"
		r, err := b.Send(ctx, id, prompt, nil, nil, source)
		eq(t, must(t, r, err)["delivery"], "steered")
		var params j.Object
		f.Edit(func(f *testserver.Fake) {
			for _, call := range f.Calls {
				if call.Method == "turn/steer" {
					params = call.Params
				}
			}
		})
		clientID := j.String(params["clientUserMessageId"])
		if clientID == "" || seen[clientID] {
			t.Fatal("missing or reused client message ID")
		}
		seen[clientID] = true
		if source != "" {
			prompt = delegationText(source, prompt)
		}
		eq(t, params["input"], input(prompt))
		turn := j.Map(j.List(f.Thread(id)["turns"])[0])
		items := j.List(turn["items"])
		message := j.Map(items[len(items)-1])
		eq(t, message["clientId"], clientID)
		eq(t, message["content"], params["input"])
	}
	eq(t, f.Params("turn/start")["clientUserMessageId"], nil)
	eq(t, f.Count("turn/steer"), 3)
}
func TestCreationResponseLoss(t *testing.T) {
	for _, method := range []string{"thread/start", "thread/name/set", "turn/start"} {
		t.Run(method, func(t *testing.T) {
			b, f, dir := setup(t)
			f.Edit(func(f *testserver.Fake) { f.Drop = method })
			r := create(t, b, j.Object{"cwd": dir, "prompt": "once", "title": "Retained"})
			eq(t, r["status"], "outcome_unknown")
			eq(t, f.Count(method), 1)
			_, known := r["threadId"]
			eq(t, known, method != "thread/start")
			f.Edit(func(f *testserver.Fake) { eq(t, len(f.Threads), 1) })
		})
	}
}
func TestPartialCreationPreservesError(t *testing.T) {
	b, f, dir := setup(t)
	reject := j.Object{"code": -42, "message": "name rejected", "data": j.Object{"detail": 1}}
	f.Edit(func(f *testserver.Fake) { f.Reject["thread/name/set"] = reject })
	r := create(t, b, j.Object{"cwd": dir, "title": "Rejected", "prompt": "not sent"})
	eq(t, r["status"], "failed")
	eq(t, r["rpcError"], reject)
	if f.Thread(j.String(r["threadId"])) == nil {
		t.Fatal("lost thread")
	}
	eq(t, f.Count("turn/start"), 0)
}
func TestMetadataAndRunningArchive(t *testing.T) {
	b, f, dir := setup(t)
	id := f.Add(dir, j.Object{"status": j.Object{"type": "active"}})
	r, e := b.Metadata(ctx, "archived", id, true)
	eq(t, must(t, r, e)["status"], "failed")
	eq(t, f.Count("thread/archive"), 0)
	eq(t, f.Count("turn/interrupt"), 0)
	f.Edit(func(f *testserver.Fake) { f.Threads[id]["status"] = j.Object{"type": "idle"} })
	for k, v := range (j.Object{"title": "Saved", "pinned": true, "archived": true}) {
		r, e = b.Metadata(ctx, k, id, v)
		eq(t, must(t, r, e)["status"], "accepted")
	}
	client := rpc.New(f.Socket)
	defer client.Close()
	fresh := New(client)
	r, e = fresh.Threads(ctx, 20, "", nil, true)
	eq(t, j.Map(j.List(must(t, r, e)["data"])[0])["name"], "Saved")
	r, e = fresh.Threads(ctx, 20, "", nil, false)
	eq(t, must(t, r, e)["data"], []any{})
	eq(t, f.Thread(id)["section"], PinnedSectionID)
	for _, k := range []string{"pinned", "archived"} {
		r, e = fresh.Metadata(ctx, k, id, false)
		eq(t, must(t, r, e)["status"], "accepted")
	}
	eq(t, f.Thread(id)["section"], nil)
}
func TestMutationLossNoReplay(t *testing.T) {
	for _, tc := range []struct {
		op, method string
		value      any
	}{{"title", "thread/name/set", "title"}, {"pinned", "thread/section/move", true}, {"archived", "thread/archive", true}, {"archived", "thread/unarchive", false}, {"send", "turn/start", "message"}} {
		t.Run(tc.method, func(t *testing.T) {
			b, f, dir := setup(t)
			id := f.Add(dir, nil)
			f.Edit(func(f *testserver.Fake) { f.Drop = tc.method })
			var r j.Object
			if tc.op == "send" {
				r = send(t, b, id, "message")
			} else {
				v, e := b.Metadata(ctx, tc.op, id, tc.value)
				r = must(t, v, e)
			}
			eq(t, r["status"], "outcome_unknown")
			eq(t, r["threadId"], id)
			eq(t, f.Count(tc.method), 1)
		})
	}
}
func TestPaginationAndClipping(t *testing.T) {
	b, f, dir := setup(t)
	for range 3 {
		create(t, b, j.Object{"cwd": dir, "prompt": strings.Repeat("界", 300)})
	}
	r, e := b.Threads(ctx, 1, "", nil, false)
	one := must(t, r, e)
	r, e = b.Threads(ctx, 1, j.String(one["nextCursor"]), nil, false)
	two := must(t, r, e)
	id := j.String(j.Map(j.List(one["data"])[0])["id"])
	if id == j.String(j.Map(j.List(two["data"])[0])["id"]) {
		t.Fatal("pagination")
	}
	send(t, b, id, "latest")
	r, e = b.Read(ctx, id, 1, "", 100, false)
	p := must(t, r, e)
	r, e = b.Read(ctx, id, 1, j.String(p["nextCursor"]), 100, false)
	p = must(t, r, e)
	text := j.String(j.Map(j.List(j.Map(j.List(p["turns"])[0])["messages"])[0])["text"])
	eq(t, text, strings.Repeat("界", 100)+"\n[truncated; original length 300 characters]")
	eq(t, f.Count("thread/resume"), 0)
}
func TestProjects(t *testing.T) {
	b, f, _ := setup(t)
	if _, e := b.Projects(ctx, 20, ""); e == nil || !strings.Contains(e.Error(), "Desktop required") {
		t.Fatal(e)
	}
	f.Edit(func(f *testserver.Fake) { f.Projects = []any{j.Object{"id": "p1"}, j.Object{"id": "p2"}} })
	r, e := b.Projects(ctx, 1, "")
	p := must(t, r, e)
	r, e = b.Projects(ctx, 1, j.String(p["nextCursor"]))
	eq(t, must(t, r, e)["data"], []any{j.Object{"id": "p2"}})
	f.Edit(func(f *testserver.Fake) { f.Reject["project/list"] = j.Object{"code": -1, "message": "unavailable"} })
	if _, e = b.Projects(ctx, 20, ""); e == nil || !strings.Contains(e.Error(), "unavailable") {
		t.Fatal(e)
	}
}
func TestMultiWaitAndRestart(t *testing.T) {
	b, f, dir := setup(t)
	r := create(t, b, j.Object{"cwd": dir, "prompt": "complete"})
	id := j.String(r["threadId"])
	other := f.Add(dir, j.Object{"status": j.Object{"type": "active", "activeFlags": []any{"waitingOnApproval"}}})
	r = wait(t, b, []any{j.Object{"threadId": id}, j.Object{"threadId": other}}, 0)
	rows := j.List(r["threads"])
	eq(t, j.Map(rows[0])["outcome"], "completed")
	eq(t, j.Map(rows[1])["outcome"], "interaction_required")
	targets := []any{}
	for _, row := range rows {
		s := j.Map(row)
		targets = append(targets, j.Object{"threadId": s["threadId"], "afterCursor": s["cursor"]})
	}
	client := rpc.New(f.Socket)
	defer client.Close()
	fresh := New(client)
	r = wait(t, fresh, targets, 1)
	eq(t, r["timedOut"], true)
	for _, row := range j.List(r["threads"]) {
		s := j.Map(row)
		eq(t, s["unchanged"], true)
		if _, ok := j.Map(s["progress"])["finalText"]; ok {
			t.Fatal("unchanged final text included")
		}
	}
	f.Edit(func(f *testserver.Fake) { f.Threads[other]["status"] = j.Object{"type": "idle"} })
	r = wait(t, fresh, targets, 0)
	eq(t, j.Map(j.List(r["threads"])[1])["outcome"], "completed")
	eq(t, f.Count("thread/resume"), 0)
}
func TestWaitValidationAndErrors(t *testing.T) {
	b, f, dir := setup(t)
	id := f.Add(dir, j.Object{"status": j.Object{"type": "active"}})
	r := wait(t, b, []any{j.Object{"threadId": id}}, 1)
	eq(t, r["timedOut"], true)
	eq(t, first(r)["outcome"], "running")
	missing := wait(t, b, []any{j.Object{"threadId": "missing"}}, 0)
	eq(t, j.Map(j.Map(j.List(missing["errors"])[0])["rpcError"])["code"], -32602)
	for _, targets := range [][]any{{}, make([]any, 9), {j.Object{"threadId": id}, j.Object{"threadId": id}}, {j.Object{"threadId": id, "afterCursor": "bad"}}, {j.Object{"threadId": "other", "afterCursor": first(r)["cursor"]}}} {
		if _, e := b.Wait(ctx, targets, 0); e == nil {
			t.Fatal("accepted invalid targets")
		}
	}
}
func TestWaitCompletion(t *testing.T) {
	b, f, dir := setup(t)
	id := f.Add(dir, j.Object{"status": j.Object{"type": "active"}})
	done := make(chan struct{})
	go func() {
		defer close(done)
		time.Sleep(10 * time.Millisecond)
		f.Edit(func(f *testserver.Fake) { f.Threads[id]["status"] = j.Object{"type": "idle"} })
	}()
	r := wait(t, b, []any{j.Object{"threadId": id}}, 1000)
	<-done
	eq(t, first(r)["outcome"], "completed")
	eq(t, r["timedOut"], false)
}
func record(t *testing.T, dir, id string) string {
	t.Helper()
	p := filepath.Join(dir, "rollout.jsonl")
	meta, _ := json.Marshal(j.Object{"type": "session_meta", "payload": j.Object{"id": id}})
	turn, _ := json.Marshal(j.Object{"type": "turn_context", "payload": j.Object{"cwd": dir, "model": "saved-model", "effort": "low", "approval_policy": "on-request", "approvals_reviewer": "user", "sandbox_policy": j.Object{"type": "read-only"}, "workspace_roots": []any{dir}}})
	if e := os.WriteFile(p, append(append(meta, '\n'), append(turn, '\n')...), 0600); e != nil {
		t.Fatal(e)
	}
	return p
}
func TestUnloadedPermissionRestoration(t *testing.T) {
	b, f, dir := setup(t)
	id := f.Add(dir, j.Object{"status": j.Object{"type": "notLoaded"}})
	path := record(t, dir, id)
	f.Edit(func(f *testserver.Fake) { f.Threads[id]["path"] = path })
	r := send(t, b, id, "safe")
	eq(t, r["status"], "accepted")
	p := f.Params("thread/resume")
	eq(t, p["sandbox"], "read-only")
	eq(t, p["approvalPolicy"], "on-request")
	eq(t, p["model"], "saved-model")
	eq(t, j.Map(p["config"])["model_reasoning_effort"], "low")
}
func TestPermissionMismatchAndMissingContext(t *testing.T) {
	b, f, dir := setup(t)
	id := f.Add(dir, j.Object{"status": j.Object{"type": "notLoaded"}})
	eq(t, send(t, b, id, "not sent")["status"], "failed")
	eq(t, f.Count("thread/resume"), 0)
	path := record(t, dir, id)
	f.Edit(func(f *testserver.Fake) {
		f.Threads[id]["path"] = path
		f.Override = j.Object{"sandbox": j.Object{"type": "dangerFullAccess"}}
	})
	r := send(t, b, id, "not sent")
	eq(t, r["status"], "failed")
	eq(t, r["messageSent"], false)
	eq(t, f.Count("turn/start"), 0)
}
func TestResumeResponseLoss(t *testing.T) {
	b, f, dir := setup(t)
	id := f.Add(dir, j.Object{"status": j.Object{"type": "notLoaded"}})
	path := record(t, dir, id)
	f.Edit(func(f *testserver.Fake) { f.Threads[id]["path"] = path; f.Drop = "thread/resume" })
	r := send(t, b, id, "not sent")
	eq(t, r["status"], "outcome_unknown")
	eq(t, r["threadId"], id)
	eq(t, f.Count("thread/resume"), 1)
	eq(t, f.Count("turn/start"), 0)
}
func TestUnavailableServer(t *testing.T) {
	dir := t.TempDir()
	c := rpc.New(filepath.Join(dir, "missing.sock"))
	defer c.Close()
	c.Timeout = 10 * time.Millisecond
	r := create(t, New(c), j.Object{"cwd": dir, "prompt": "not sent"})
	eq(t, r["status"], "failed")
	if !strings.Contains(j.String(r["error"]), "was not sent") {
		t.Fatal(r)
	}
	eq(t, r["threadId"], nil)
}
func TestSteerLossRetainsTurnID(t *testing.T) {
	b, f, dir := setup(t)
	f.Edit(func(f *testserver.Fake) { f.Complete = false })
	created := create(t, b, j.Object{"cwd": dir, "prompt": "start"})
	f.Edit(func(f *testserver.Fake) { f.Drop = "turn/steer" })
	r := send(t, b, j.String(created["threadId"]), "once")
	eq(t, r["status"], "outcome_unknown")
	eq(t, r["turnId"], created["turnId"])
	eq(t, f.Count("turn/steer"), 1)
	eq(t, f.Count("turn/start"), 1)
}
func TestWaitFlags(t *testing.T) {
	for _, flag := range []string{"waitingOnUserInput", "waitingOnApproval"} {
		t.Run(flag, func(t *testing.T) {
			b, f, dir := setup(t)
			id := f.Add(dir, j.Object{"status": j.Object{"type": "active", "activeFlags": []any{flag}}})
			eq(t, first(wait(t, b, []any{j.Object{"threadId": id}}, 0))["outcome"], "interaction_required")
		})
	}
}
func TestWaitTerminalErrors(t *testing.T) {
	for _, status := range []string{"failed", "interrupted"} {
		t.Run(status, func(t *testing.T) {
			b, f, dir := setup(t)
			id := f.Add(dir, j.Object{"turns": []any{j.Object{"id": "turn1", "status": status, "items": []any{}, "error": j.Object{"message": "stopped"}}}})
			s := first(wait(t, b, []any{j.Object{"threadId": id}}, 0))
			eq(t, s["outcome"], status)
			eq(t, j.Map(j.Map(s["progress"])["error"])["message"], "stopped")
		})
	}
}
func TestWaitPreviousFailureWhileActive(t *testing.T) {
	b, f, dir := setup(t)
	id := f.Add(dir, j.Object{"status": j.Object{"type": "active"}, "turns": []any{j.Object{"id": "old", "status": "failed", "items": []any{}}}})
	eq(t, first(wait(t, b, []any{j.Object{"threadId": id}}, 0))["outcome"], "running")
}
func TestMissingRollout(t *testing.T) {
	b, f, dir := setup(t)
	id := f.Add(dir, j.Object{"status": j.Object{"type": "notLoaded"}, "path": filepath.Join(dir, "missing.jsonl")})
	r := send(t, b, id, "not sent")
	eq(t, r["status"], "failed")
	if !strings.Contains(j.String(r["error"]), "Cannot read saved task settings") {
		t.Fatal(r)
	}
	eq(t, f.Count("thread/resume"), 0)
	eq(t, f.Count("turn/start"), 0)
}
func TestWorktreeNoSideEffects(t *testing.T) {
	b, f, dir := setup(t)
	_, err := b.Create(ctx, j.Object{"cwd": dir, "environment": j.Object{"type": "worktree"}}, "")
	if err == nil || !strings.Contains(err.Error(), "Worktree support") {
		t.Fatal(err)
	}
	eq(t, f.Count("initialize"), 0)
	entries, _ := os.ReadDir(dir)
	eq(t, len(entries), 0)
}
func TestPythonCursorFixtures(t *testing.T) {
	raw, e := os.ReadFile("testdata/python_snapshots.json")
	if e != nil {
		t.Fatal(e)
	}
	var fixtures []j.Object
	if e = j.Decode(raw, &fixtures); e != nil {
		t.Fatal(e)
	}
	b := New(rpc.New("/reference/app.sock"))
	for _, f := range fixtures {
		encoded := j.PythonJSON(f["snapshot"])
		eq(t, encoded, f["json"])
		digest := fmt.Sprintf("%x", sha256.Sum256([]byte(encoded)))
		eq(t, digest, f["digest"])
		_, e := b.decodeCursor(j.String(f["cursor"]), "thread-1")
		if e == nil {
			t.Fatal("accepted historical cursor")
		}
	}
}
