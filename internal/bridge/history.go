package bridge

import (
	"encoding/json"
	"strings"

	j "github.com/JesseSenior/codex-thread-bridge/internal/jsonutil"
)

func textContent(v any) string {
	var parts []string
	for _, value := range j.List(v) {
		item := j.Map(value)
		if item["type"] == "text" {
			parts = append(parts, j.String(item["text"]))
		}
	}
	return strings.Join(parts, "\n")
}

func compactTurn(turn j.Object, chars int, outputs bool) j.Object {
	result := j.Object{"id": turn["id"], "status": turn["status"]}
	if err := j.Map(turn["error"]); err != nil {
		result["error"] = j.Object{"message": Clip(err["message"], chars, true)}
	}
	messages, tools := []any{}, []any{}
	for _, v := range j.List(turn["items"]) {
		item := j.Map(v)
		kind := j.String(item["type"])
		switch kind {
		case "reasoning":
			continue
		case "userMessage", "agentMessage", "plan":
			role, text := "assistant", j.String(item["text"])
			if kind == "userMessage" {
				role, text = "user", textContent(item["content"])
			}
			message := j.Object{"id": item["id"], "role": role, "text": Clip(text, chars, true)}
			if kind == "agentMessage" && item["phase"] != nil {
				message["phase"] = item["phase"]
			}
			if kind == "plan" {
				message["phase"] = "plan"
			}
			messages = append(messages, message)
		default:
			// Only tool types are included; new protocol items are not raw output.
			switch kind {
			case "commandExecution", "fileChange", "mcpToolCall", "dynamicToolCall", "webSearch", "imageView", "imageGeneration", "collabAgentToolCall", "functionCallOutput", "subAgentActivity", "sleep":
			default:
				continue
			}
			summary := j.Object{"id": item["id"], "type": kind, "status": item["status"]}
			for _, key := range []string{"command", "server", "namespace", "name", "tool", "query", "exitCode", "path", "agentThreadId", "durationMs"} {
				if value, ok := item[key]; ok {
					summary[key] = Clip(value, chars, true)
				}
			}
			if kind == "fileChange" {
				paths := []any{}
				for _, change := range j.List(item["changes"]) {
					paths = append(paths, Clip(j.Map(change)["path"], chars, true))
				}
				summary["paths"] = paths
			}
			if outputs {
				out := j.Object{}
				for _, key := range []string{"aggregatedOutput", "result", "content", "contentItems", "output", "changes", "results", "error"} {
					if value, ok := item[key]; ok {
						text, ok := value.(string)
						if !ok {
							raw, _ := json.Marshal(value)
							text = string(raw)
						}
						out[key] = Clip(text, chars, true)
					}
				}
				if len(out) > 0 {
					summary["output"] = out
				}
			}
			tools = append(tools, summary)
		}
	}
	result["messages"], result["tools"] = messages, tools
	return result
}

// progress contains display text only. The raw final text is returned for hashing.
func progress(turn j.Object) (j.Object, string) {
	p := j.Object{"turnId": turn["id"], "turnStatus": turn["status"]}
	var final, unphased, commentary []string
	for _, value := range j.List(turn["items"]) {
		item := j.Map(value)
		if item["type"] != "agentMessage" {
			continue
		}
		text := j.String(item["text"])
		switch item["phase"] {
		case "commentary":
			commentary = append(commentary, text)
		case "final_answer":
			final = append(final, text)
		default:
			unphased = append(unphased, text)
		}
	}
	if len(commentary) > 0 {
		p["latestCommentary"] = Clip(commentary[len(commentary)-1], 4000, true)
	}
	terminal := turn["status"] == "completed" || turn["status"] == "failed" || turn["status"] == "interrupted"
	if terminal && len(final) == 0 {
		final = unphased
	}
	text := ""
	if terminal {
		text = strings.Join(final, "\n")
	}
	if text != "" {
		p["finalText"] = Clip(text, 4000, true)
	}
	if err := j.Map(turn["error"]); err != nil {
		p["error"] = j.Object{"message": Clip(err["message"], 4000, true)}
	}
	return p, text
}
