package bridge

import (
	"fmt"

	j "github.com/JesseSenior/codex-thread-bridge/internal/jsonutil"
	"github.com/JesseSenior/codex-thread-bridge/internal/settings"
)

// resolveModel validates explicit overrides against a fresh server catalog.
func resolveModel(call callFunc, model, effort any, thread j.Object, cwd string) (string, string, error) {
	for key, value := range map[string]any{"model": model, "thinking": effort} {
		if value != nil {
			if err := nonempty(value, key, 100000); err != nil {
				return "", "", err
			}
		}
	}
	if thread == nil && (model == nil || effort == nil) {
		v, err := call("config/read", j.Object{"cwd": cwd, "includeLayers": false})
		if err != nil {
			return "", "", err
		}
		config := j.Map(v["config"])
		profile := j.Map(j.Map(config["profiles"])[j.String(config["profile"])])
		for _, key := range []string{"model", "model_reasoning_effort"} {
			if profile[key] != nil {
				config[key] = profile[key]
			}
		}
		if model == nil {
			model = config["model"]
		}
		if effort == nil {
			effort = config["model_reasoning_effort"]
		}
	}
	if thread != nil {
		if model == nil {
			model = thread["model"]
		}
		if effort == nil {
			effort = thread["reasoningEffort"]
		}
		if model == nil || effort == nil {
			p, _, err := settings.Saved(thread)
			if err != nil {
				return "", "", err
			}
			if model == nil {
				model = p["model"]
			}
			if effort == nil {
				effort = j.Map(p["config"])["model_reasoning_effort"]
			}
		}
		if model == nil || effort == nil {
			return "", "", settings.Fail("Saved model and thinking could not be established")
		}
	}
	var models []any
	cursor := ""
	seen := map[string]bool{}
	for {
		p := j.Object{"includeHidden": true, "limit": 100}
		if cursor != "" {
			p["cursor"] = cursor
		}
		page, err := call("model/list", p)
		if err != nil {
			return "", "", err
		}
		models = append(models, j.List(page["data"])...)
		cursor = j.String(page["nextCursor"])
		if cursor == "" {
			break
		}
		if seen[cursor] {
			return "", "", settings.Fail("Model catalog repeated a page cursor")
		}
		seen[cursor] = true
	}
	for _, value := range models {
		m := j.Map(value)
		if (model == nil && m["isDefault"] == true) || (model != nil && (model == m["model"] || model == m["id"])) {
			if effort == nil {
				effort = m["defaultReasoningEffort"]
			}
			for _, v := range j.List(m["supportedReasoningEfforts"]) {
				if effort != nil && effort == j.Map(v)["reasoningEffort"] && j.String(m["model"]) != "" {
					return j.String(m["model"]), j.String(effort), nil
				}
			}
			return "", "", settings.Fail(fmt.Sprintf("Unsupported thinking %v for model %v", effort, model))
		}
	}
	return "", "", settings.Fail(fmt.Sprintf("Model %v is unavailable in the server catalog", model))
}
