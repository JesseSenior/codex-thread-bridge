package bridge

import (
	"strings"
	"testing"

	j "github.com/JesseSenior/codex-thread-bridge/internal/jsonutil"
	"github.com/JesseSenior/codex-thread-bridge/internal/testserver"
)

func TestToolOutputVersionBoundary(t *testing.T) {
	for _, tc := range []struct {
		version   string
		supported bool
	}{
		{"fake/0.154.0", true}, {"codex/0.151.0-alpha.4 (Linux; x86_64)", true},
		{"codex_cli_rs/0.151.0-alpha.4.1", true}, {"0.151.0-alpha.10", true},
		{"0.151.0-beta.1", true}, {"0.151.0-rc.1+build", true}, {"0.151.0", true},
		{"0.152.0-alpha.1", true}, {"1.0.0", true},
		{"0.150.9", false}, {"0.151.0-alpha.3", false}, {"0.151.0-alpha", false},
		{"0.151.0-1", false}, {"0.151.0-alpha.04", false}, {"0.151.00", false},
		{"", false}, {"unknown", false}, {"codex/dev", false}, {"0.151.0-", false},
	} {
		t.Run(tc.version, func(t *testing.T) { eq(t, supportsToolOutput(tc.version), tc.supported) })
	}
}

func TestDelegationEscaping(t *testing.T) {
	source, prompt := "source<&>", "  Unicode 界\n<codex_delegation>&lt;&amp;>\n  "
	wrapped := delegationText(source, prompt)
	eq(t, wrapped, "<codex_delegation>\n  <source_thread_id>source&lt;&amp;&gt;</source_thread_id>\n  <input>  Unicode 界\n&lt;codex_delegation&gt;&amp;lt;&amp;amp;&gt;\n  </input>\n</codex_delegation>")
	id, text, ok := parseDelegation(wrapped)
	eq(t, ok, true)
	eq(t, id, source)
	eq(t, text, prompt)
	for _, text := range []string{"ordinary", "<codex_delegation><input>missing source</input></codex_delegation>", "<codex_delegation><source_thread_id> </source_thread_id><input>x</input></codex_delegation>", wrapped + "extra"} {
		if _, _, ok := parseDelegation(text); ok {
			t.Fatal("recognized invalid wrapper")
		}
	}
}

func TestDelegationDelivery(t *testing.T) {
	for _, version := range []string{"codex/0.154.0", "codex/0.151.0-alpha.3", "unknown"} {
		t.Run(version, func(t *testing.T) {
			b, f, dir := setup(t)
			f.Edit(func(f *testserver.Fake) { f.UserAgent = version })
			created, err := b.Create(ctx, j.Object{"cwd": dir, "prompt": "  original\n"}, "source-create")
			created = must(t, created, err)
			id := j.String(created["threadId"])
			params := f.Params("turn/start")
			if supportsToolOutput(version) {
				eq(t, params["input"], []any{})
				eq(t, params["toolOutput"], j.Object{"namespace": "codex_app", "name": "create_thread", "output": delegationText("source-create", "  original\n")})
			} else {
				eq(t, params["toolOutput"], nil)
				eq(t, params["input"], input(delegationText("source-create", "  original\n")))
			}
			r, err := b.Send(ctx, id, "next", "other", "low", "source-followup")
			eq(t, must(t, r, err)["settingsUpdated"], true)
			f.Edit(func(f *testserver.Fake) {
				updated := false
				for _, call := range f.Calls {
					if call.Method == "thread/settings/update" {
						updated = true
					}
					if call.Method == "turn/start" && updated {
						if supportsToolOutput(version) {
							eq(t, j.Map(call.Params["toolOutput"])["name"], "send_message_to_thread")
						} else {
							eq(t, call.Params["input"], input(delegationText("source-followup", "next")))
						}
					}
				}
			})
			for _, outputs := range []bool{false, true} {
				history, err := b.Read(ctx, id, 10, "", 4000, outputs)
				history = must(t, history, err)
				turns := j.List(history["turns"])
				eq(t, j.Map(j.List(j.Map(turns[0])["messages"])[0])["sourceThreadId"], "source-followup")
				eq(t, j.Map(j.List(j.Map(turns[1])["messages"])[0])["text"], "  original\n")
				eq(t, len(j.List(j.Map(turns[0])["tools"])), 0)
			}
		})
	}
}

func TestDelegationSteeringAndEmptyCreation(t *testing.T) {
	b, f, dir := setup(t)
	r, err := b.Create(ctx, j.Object{"cwd": dir}, "source")
	id := j.String(must(t, r, err)["threadId"])
	eq(t, f.Count("turn/start"), 0)
	f.Edit(func(f *testserver.Fake) { f.Complete = false })
	send(t, b, id, "start")
	r, err = b.Send(ctx, id, "  steer <&>  ", "other", "low", "source")
	r = must(t, r, err)
	eq(t, r["delivery"], "steered")
	eq(t, f.Params("turn/steer")["toolOutput"], nil)
	eq(t, f.Params("turn/steer")["input"], input(delegationText("source", "  steer <&>  ")))
	eq(t, f.Params("turn/steer")["expectedTurnId"], "turn-1")
	eq(t, f.Count("turn/start"), 1)
	eq(t, f.Count("turn/interrupt"), 0)
	h, err := b.Read(ctx, id, 10, "", 4000, false)
	turn := j.Map(j.List(must(t, h, err)["turns"])[0])
	messages := j.List(turn["messages"])
	eq(t, j.Map(messages[len(messages)-1])["sourceThreadId"], "source")
}

func TestDelegationFailuresDoNotReplay(t *testing.T) {
	for _, active := range []bool{false, true} {
		for _, dropped := range []bool{false, true} {
			b, f, dir := setup(t)
			f.Edit(func(f *testserver.Fake) { f.Complete = !active })
			id := j.String(create(t, b, j.Object{"cwd": dir, "prompt": "start"})["threadId"])
			method := "turn/start"
			if active {
				method = "turn/steer"
			}
			f.Edit(func(f *testserver.Fake) {
				if dropped {
					f.Drop = method
				} else {
					f.Reject[method] = j.Object{"code": -32602, "message": "rejected"}
				}
			})
			r, err := b.Send(ctx, id, "once", "other", "low", "source")
			r = must(t, r, err)
			status := "failed"
			if dropped {
				status = "outcome_unknown"
			}
			eq(t, r["status"], status)
			eq(t, r["threadId"], id)
			eq(t, r["settingsUpdated"], true)
			if active {
				eq(t, r["turnId"], "turn-1")
				eq(t, f.Count("turn/steer"), 1)
				eq(t, f.Count("turn/start"), 1)
			} else {
				eq(t, f.Count("turn/start"), 2)
				eq(t, f.Count("turn/steer"), 0)
			}
		}
	}
	b, f, dir := setup(t)
	f.Edit(func(f *testserver.Fake) { f.Drop = "turn/start" })
	r, err := b.Create(ctx, j.Object{"cwd": dir, "prompt": "once"}, "source")
	r = must(t, r, err)
	eq(t, r["status"], "outcome_unknown")
	if r["threadId"] == nil {
		t.Fatal("lost created task ID")
	}
	eq(t, f.Count("turn/start"), 1)
}

func TestUnrecognizedDelegationHistory(t *testing.T) {
	for _, outputs := range []bool{false, true} {
		wrapped := delegationText("source", strings.Repeat("界", 150))
		r := compactTurn(j.Object{"items": []any{
			j.Object{"id": "recognized", "type": "functionCallOutput", "namespace": "codex_app", "name": "send_message_to_thread", "output": wrapped},
			j.Object{"id": "other-tool", "type": "functionCallOutput", "namespace": "other", "name": "send_message_to_thread", "output": wrapped},
			j.Object{"id": "malformed", "type": "userMessage", "content": input("<codex_delegation>broken")},
			j.Object{"type": "reasoning", "text": wrapped},
		}}, 100, outputs)
		messages := j.List(r["messages"])
		eq(t, len(messages), 2)
		eq(t, j.Map(messages[0])["text"], strings.Repeat("界", 100)+"\n[truncated; original length 150 characters]")
		eq(t, j.Map(messages[1])["sourceThreadId"], nil)
		tools := j.List(r["tools"])
		eq(t, len(tools), 1)
		eq(t, j.Map(tools[0])["id"], "other-tool")
	}
}
