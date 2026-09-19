package bridge

import (
	"encoding/base64"
	"strings"
	"testing"
	"time"

	j "github.com/JesseSenior/codex-thread-bridge/internal/jsonutil"
	"github.com/JesseSenior/codex-thread-bridge/internal/testserver"
)

func TestCatalogValidation(t *testing.T) {
	for _, tc := range []struct {
		name   string
		args   j.Object
		reject string
	}{
		{"unknown", j.Object{"model": "missing", "thinking": "low"}, ""},
		{"effort", j.Object{"model": "other", "thinking": "invalid"}, ""},
		{"catalog", j.Object{"model": "other", "thinking": "low"}, "model/list"},
		{"config", j.Object{"thinking": "low"}, "config/read"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b, f, dir := setup(t)
			if tc.reject != "" {
				f.Edit(func(f *testserver.Fake) { f.Reject[tc.reject] = j.Object{"code": -32601, "message": "unavailable"} })
			}
			tc.args["cwd"] = dir
			if _, err := b.Create(ctx, tc.args, ""); err == nil {
				t.Fatal("invalid override accepted")
			}
			eq(t, f.Count("thread/start"), 0)
			eq(t, f.Count("turn/start"), 0)
		})
	}
}

func TestCatalogPaginationAndDefaults(t *testing.T) {
	for _, tc := range []struct {
		args          j.Object
		model, effort string
	}{
		{j.Object{"model": "other"}, "other", "medium"},
		{j.Object{"thinking": "low"}, "server-default", "low"},
		{j.Object{"model": "saved-model", "thinking": "high"}, "saved-model", "high"},
	} {
		b, f, dir := setup(t)
		f.Edit(func(f *testserver.Fake) { f.ModelPageSize = 1 })
		tc.args["cwd"] = dir
		r := create(t, b, tc.args)
		eq(t, j.Map(r["creation"])["model"], tc.model)
		eq(t, j.Map(r["creation"])["reasoningEffort"], tc.effort)
		eq(t, f.Count("model/list"), 3)
		eq(t, f.Params("model/list")["includeHidden"], true)
	}
	b, f, dir := setup(t)
	f.Edit(func(f *testserver.Fake) { f.Config = j.Object{} })
	r := create(t, b, j.Object{"cwd": dir, "model": "other"})
	eq(t, j.Map(r["creation"])["reasoningEffort"], "medium")
}

func TestFollowupOverrides(t *testing.T) {
	for _, active := range []bool{false, true} {
		b, f, dir := setup(t)
		f.Edit(func(f *testserver.Fake) { f.Complete = !active })
		created := create(t, b, j.Object{"cwd": dir, "prompt": "start"})
		id := j.String(created["threadId"])
		r, err := b.Send(ctx, id, "follow", "other", "low", "")
		r = must(t, r, err)
		eq(t, r["settingsUpdated"], true)
		eq(t, r["messageSent"], true)
		eq(t, f.Params("thread/settings/update"), j.Object{"threadId": id, "model": "other", "effort": "low"})
		eq(t, f.Count("turn/interrupt"), 0)
		turns := j.List(f.Thread(id)["turns"])
		eq(t, j.Map(turns[0])["model"], "server-default")
		eq(t, j.Map(turns[0])["effort"], "medium")
		if active {
			eq(t, r["delivery"], "steered")
			eq(t, len(turns), 1)
			f.Edit(func(f *testserver.Fake) {
				f.Complete = true
				f.Threads[id]["status"] = j.Object{"type": "idle"}
				j.Map(j.List(f.Threads[id]["turns"])[0])["status"] = "completed"
			})
			send(t, b, id, "next")
		} else {
			eq(t, r["delivery"], "started")
		}
		turns = j.List(f.Thread(id)["turns"])
		eq(t, j.Map(turns[1])["model"], "other")
		eq(t, j.Map(turns[1])["effort"], "low")
		f.Edit(func(f *testserver.Fake) {
			updated := false
			for _, c := range f.Calls {
				if c.Method == "thread/settings/update" {
					updated = true
				}
				if (c.Method == "turn/steer" || c.Method == "turn/start") && j.Map(j.List(c.Params["input"])[0])["text"] == "follow" && !updated {
					t.Error("message before settings update")
				}
			}
		})
	}
}

func TestUnloadedOverridesAndMissingSettings(t *testing.T) {
	b, f, dir := setup(t)
	id := f.Add(dir, j.Object{"status": j.Object{"type": "notLoaded"}})
	r, err := b.Send(ctx, id, "fail", "other", nil, "")
	eq(t, must(t, r, err)["status"], "failed")
	eq(t, f.Count("thread/resume"), 0)
	path := record(t, dir, id)
	f.Edit(func(f *testserver.Fake) { f.Threads[id]["path"] = path })
	r, err = b.Send(ctx, id, "safe", "other", nil, "")
	eq(t, must(t, r, err)["status"], "accepted")
	eq(t, f.Params("thread/settings/update")["effort"], "low")
	eq(t, f.Params("thread/resume")["model"], "saved-model")
	f.Edit(func(f *testserver.Fake) {
		resumed := false
		for _, c := range f.Calls {
			if c.Method == "thread/resume" {
				resumed = true
			}
			if c.Method == "thread/settings/update" && !resumed {
				t.Error("update before resume")
			}
		}
	})
	r, err = b.Send(ctx, id, "effort only", nil, "high", "")
	eq(t, must(t, r, err)["settings"], j.Object{"model": "other", "thinking": "high"})
}

func TestOverrideFailures(t *testing.T) {
	for _, tc := range []struct {
		method string
		drop   bool
		active bool
	}{
		{"model/list", false, false},
		{"model/list", true, false},
		{"thread/settings/update", false, false},
		{"thread/settings/update", true, false},
		{"turn/start", false, false},
		{"turn/start", true, false},
		{"turn/steer", false, true},
		{"turn/steer", true, true},
	} {
		b, f, dir := setup(t)
		f.Edit(func(f *testserver.Fake) { f.Complete = !tc.active })
		id := j.String(create(t, b, j.Object{"cwd": dir, "prompt": "start"})["threadId"])
		f.Edit(func(f *testserver.Fake) {
			if tc.drop {
				f.Drop = tc.method
			} else {
				f.Reject[tc.method] = j.Object{"code": -32601, "message": "unsupported"}
			}
		})
		r, err := b.Send(ctx, id, "once", "other", "low", "")
		r = must(t, r, err)
		wantStatus := "failed"
		if tc.drop && tc.method != "model/list" {
			wantStatus = "outcome_unknown"
		}
		eq(t, r["status"], wantStatus)
		eq(t, r["threadId"], id)
		eq(t, r["method"], tc.method)
		var updated any = tc.method == "turn/start" || tc.method == "turn/steer"
		if tc.method == "thread/settings/update" && tc.drop {
			updated = nil
		}
		eq(t, r["settingsUpdated"], updated)
		var sent any = false
		if tc.drop && (tc.method == "turn/start" || tc.method == "turn/steer") {
			sent = nil
		}
		eq(t, r["messageSent"], sent)
		if tc.method == "thread/settings/update" && !tc.drop && r["limitation"] == nil {
			t.Fatal("missing capability explanation")
		}
		if tc.method == "model/list" || tc.method == "thread/settings/update" {
			eq(t, f.Count("turn/start"), 1)
		}
		if tc.method != "turn/start" {
			eq(t, f.Count(tc.method), 1)
		} else {
			eq(t, f.Count(tc.method), 2)
		}
	}
}

func TestCompactHistory(t *testing.T) {
	b, f, dir := setup(t)
	id := f.Add(dir, j.Object{"turns": []any{j.Object{"id": "t1", "status": "completed", "items": []any{
		j.Object{"id": "u1", "type": "userMessage", "content": []any{j.Object{"type": "text", "text": "question"}}},
		j.Object{"id": "r1", "type": "reasoning", "text": "private reasoning"},
		j.Object{"id": "c1", "type": "commandExecution", "command": "test", "status": "completed", "aggregatedOutput": strings.Repeat("界", 150)},
		j.Object{"id": "a1", "type": "agentMessage", "phase": "final_answer", "text": "answer"},
	}}}})
	for _, outputs := range []bool{false, true} {
		r, err := b.Read(ctx, id, 10, "", 100, outputs)
		r = must(t, r, err)
		if strings.Contains(j.PythonJSON(r), "private reasoning") {
			t.Fatal("reasoning exposed")
		}
		turn := j.Map(j.List(r["turns"])[0])
		eq(t, len(j.List(turn["messages"])), 2)
		eq(t, j.Map(j.List(turn["messages"])[0])["text"], "question")
		tool := j.Map(j.List(turn["tools"])[0])
		eq(t, tool["command"], "test")
		if outputs {
			eq(t, j.Map(tool["output"])["aggregatedOutput"], strings.Repeat("界", 100)+"\n[truncated; original length 150 characters]")
		} else if tool["output"] != nil {
			t.Fatal("output included by default")
		}
	}
	eq(t, f.Count("thread/resume"), 0)
}

func TestWaitFinalIdentity(t *testing.T) {
	b, f, dir := setup(t)
	id := f.Add(dir, j.Object{"turns": []any{j.Object{"id": "t1", "status": "completed", "items": []any{j.Object{"type": "agentMessage", "phase": "final_answer", "text": "answer"}}}}})
	firstResult := first(wait(t, b, []any{j.Object{"threadId": id}}, 0))
	eq(t, j.Map(firstResult["progress"])["finalText"], "answer")
	f.Edit(func(f *testserver.Fake) {
		f.Threads[id]["status"] = j.Object{"type": "idle", "unrelated": true}
		turn := j.Map(j.List(f.Threads[id]["turns"])[0])
		turn["items"] = append(j.List(turn["items"]), j.Object{"type": "agentMessage", "phase": "commentary", "text": "extra commentary"})
	})
	targets := []any{j.Object{"threadId": id, "afterCursor": firstResult["cursor"]}}
	r := wait(t, b, targets, 1)
	eq(t, r["timedOut"], true)
	eq(t, j.Map(first(r)["progress"])["finalText"], nil)
	eq(t, j.Map(first(r)["progress"])["latestCommentary"], "extra commentary")
	f.Edit(func(f *testserver.Fake) {
		j.Map(j.List(j.Map(j.List(f.Threads[id]["turns"])[0])["items"])[0])["text"] = "revised"
	})
	r = wait(t, b, targets, 1)
	eq(t, r["timedOut"], false)
	eq(t, j.Map(first(r)["progress"])["finalText"], "revised")
	targets = []any{j.Object{"threadId": id, "afterCursor": first(r)["cursor"]}}
	f.Edit(func(f *testserver.Fake) { j.Map(j.List(f.Threads[id]["turns"])[0])["id"] = "t2" })
	eq(t, j.Map(first(wait(t, b, targets, 0))["progress"])["finalText"], "revised")
}

func TestWaitCommentaryDoesNotWake(t *testing.T) {
	b, f, dir := setup(t)
	id := f.Add(dir, j.Object{"status": j.Object{"type": "active"}, "turns": []any{j.Object{"id": "t1", "status": "inProgress", "items": []any{j.Object{"type": "agentMessage", "phase": "commentary", "text": "working"}}}}})
	start := time.Now()
	r := wait(t, b, []any{j.Object{"threadId": id}}, 20)
	eq(t, r["timedOut"], true)
	if time.Since(start) < 20*time.Millisecond {
		t.Fatal("commentary ended wait")
	}
	eq(t, j.Map(first(r)["progress"])["latestCommentary"], "working")
	eq(t, j.Map(first(r)["progress"])["finalText"], nil)
}

func TestWaitCursorValidation(t *testing.T) {
	b, f, dir := setup(t)
	id := f.Add(dir, nil)
	s := first(wait(t, b, []any{j.Object{"threadId": id}}, 0))
	raw, _ := base64.URLEncoding.DecodeString(j.String(s["cursor"]))
	for _, patch := range []j.Object{{"v": 1}, {"host": "/other.sock"}, {"digest": "x"}, {"final": 2}, {"event": nil}, {"extra": true}} {
		var value j.Object
		if err := j.Decode(raw, &value); err != nil {
			t.Fatal(err)
		}
		for k, v := range patch {
			value[k] = v
		}
		cursor := base64.URLEncoding.EncodeToString([]byte(j.PythonJSON(value)))
		if _, err := b.Wait(ctx, []any{j.Object{"threadId": id, "afterCursor": cursor}}, 0); err == nil {
			t.Fatal("invalid cursor accepted")
		}
	}
}

func TestOverrideValidationBeforeResume(t *testing.T) {
	b, f, dir := setup(t)
	id := f.Add(dir, j.Object{"status": j.Object{"type": "notLoaded"}})
	path := record(t, dir, id)
	f.Edit(func(f *testserver.Fake) { f.Threads[id]["path"] = path })
	r, err := b.Send(ctx, id, "not delivered", "unknown", "low", "")
	eq(t, must(t, r, err)["status"], "failed")
	eq(t, f.Count("thread/resume"), 0)
	eq(t, f.Count("thread/settings/update"), 0)
	eq(t, f.Count("turn/start"), 0)
}

func TestSavedSettingsSurviveDeliveryRace(t *testing.T) {
	for _, active := range []bool{false, true} {
		b, f, dir := setup(t)
		f.Edit(func(f *testserver.Fake) { f.Complete = !active })
		id := j.String(create(t, b, j.Object{"cwd": dir, "prompt": "start"})["threadId"])
		f.Edit(func(f *testserver.Fake) {
			f.Before = func(method string, _ j.Object) {
				if active && method == "turn/steer" {
					f.Threads[id]["status"] = j.Object{"type": "idle"}
				}
				if !active && method == "turn/start" {
					f.Threads[id]["status"] = j.Object{"type": "active"}
				}
			}
		})
		r, err := b.Send(ctx, id, "race", "other", "low", "")
		r = must(t, r, err)
		eq(t, r["status"], "failed")
		eq(t, r["settingsUpdated"], true)
		eq(t, r["messageSent"], false)
		eq(t, f.Thread(id)["model"], "other")
		eq(t, f.Count("thread/settings/update"), 1)
		if active {
			eq(t, f.Count("turn/start"), 1)
			eq(t, f.Count("turn/steer"), 1)
		} else {
			eq(t, f.Count("turn/start"), 2)
			eq(t, f.Count("turn/steer"), 0)
		}
	}
}

func TestSettingsSavedBeforeReadFailure(t *testing.T) {
	b, f, dir := setup(t)
	id := j.String(create(t, b, j.Object{"cwd": dir})["threadId"])
	f.Edit(func(f *testserver.Fake) {
		reads := 0
		f.Before = func(method string, _ j.Object) {
			if method == "thread/read" {
				reads++
				if reads == 2 {
					f.Drop = method
				}
			}
		}
	})
	r, err := b.Send(ctx, id, "not sent", "other", "low", "")
	r = must(t, r, err)
	eq(t, r["status"], "failed")
	eq(t, r["settingsUpdated"], true)
	eq(t, r["messageSent"], false)
	eq(t, r["method"], "thread/read")
	eq(t, f.Count("turn/start"), 0)
}

func TestWaitRevisionBeyondDisplayLimit(t *testing.T) {
	b, f, dir := setup(t)
	text := strings.Repeat("界", 4100)
	id := f.Add(dir, j.Object{"turns": []any{j.Object{"id": "t1", "status": "completed", "items": []any{j.Object{"type": "agentMessage", "text": text}}}}})
	before := first(wait(t, b, []any{j.Object{"threadId": id}}, 0))
	f.Edit(func(f *testserver.Fake) {
		j.Map(j.List(j.Map(j.List(f.Threads[id]["turns"])[0])["items"])[0])["text"] = strings.Repeat("界", 4099) + "a"
	})
	r := wait(t, b, []any{j.Object{"threadId": id, "afterCursor": before["cursor"]}}, 1)
	eq(t, r["timedOut"], false)
	eq(t, first(r)["unchanged"], false)
	if j.Map(first(r)["progress"])["finalText"] == nil {
		t.Fatal("revised final text suppressed")
	}
}
