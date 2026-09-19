// Package settings reads server-owned rollout files and verifies restored permissions.
package settings

import (
	"bufio"
	"fmt"
	j "github.com/JesseSenior/codex-thread-bridge/internal/jsonutil"
	"io"
	"os"
	"path/filepath"
	"strings"
)

type Error struct{ Message string }

func (e *Error) Error() string  { return e.Message }
func Fail(message string) error { return &Error{message} }

func Saved(thread j.Object) (j.Object, j.Object, error) {
	path := j.String(thread["path"])
	if !filepath.IsAbs(path) {
		return nil, nil, Fail("No saved task settings. Open the task in Desktop before messaging it.")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, Fail("Cannot read saved task settings: " + err.Error())
	}
	defer f.Close()
	reader := bufio.NewReader(f)
	var context j.Object
	var owner any
	for {
		line, readErr := reader.ReadString('\n')
		if len(line) > 0 {
			var record j.Object
			if err := j.Decode([]byte(line), &record); err != nil {
				if !strings.HasSuffix(line, "\n") && readErr == io.EOF {
					break
				}
				return nil, nil, Fail("Invalid task metadata; no message was sent")
			}
			switch record["type"] {
			case "session_meta":
				owner = j.Map(record["payload"])["id"]
			case "turn_context":
				context = j.Map(record["payload"])
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return nil, nil, Fail("Cannot read saved task settings: " + readErr.Error())
		}
	}
	if !j.Equal(owner, thread["id"]) || context == nil {
		return nil, nil, Fail("Saved task settings are unavailable; open the task in Desktop.")
	}
	for _, key := range []string{"sandbox_policy", "approval_policy", "cwd", "model", "effort"} {
		if _, ok := context[key]; !ok {
			return nil, nil, Fail("Incomplete saved task settings; no message was sent")
		}
	}
	sandbox := j.Map(context["sandbox_policy"])
	kind := j.String(sandbox["type"])
	types := map[string]string{"read-only": "readOnly", "workspace-write": "workspaceWrite", "danger-full-access": "dangerFullAccess"}
	if types[kind] == "" {
		return nil, nil, Fail(fmt.Sprintf("Cannot safely restore sandbox '%s'; use Desktop", kind))
	}
	expected := j.Object{"type": types[kind]}
	model := thread["model"]
	if model == nil || model == "" {
		model = context["model"]
	}
	config := j.Object{"model_reasoning_effort": j.Default(thread, "reasoningEffort", context["effort"])}
	params := j.Object{"threadId": thread["id"], "excludeTurns": true, "cwd": thread["cwd"], "model": model, "approvalPolicy": context["approval_policy"], "config": config}
	for source, target := range map[string]string{"approvals_reviewer": "approvalsReviewer", "workspace_roots": "runtimeWorkspaceRoots"} {
		if v, ok := context[source]; ok {
			params[target] = v
		}
	}
	if v := thread["modelProvider"]; v != nil && v != "" {
		params["modelProvider"] = v
	}
	for source, target := range map[string]string{"service_tier": "serviceTier", "personality": "personality"} {
		if v := context[source]; v != nil {
			params[target] = v
		}
	}
	if v := context["summary"]; v != nil {
		config["model_reasoning_summary"] = v
	}
	if profile := j.Map(context["active_permission_profile"]); len(profile) > 0 {
		id := profile["id"]
		if id != ":read-only" && id != ":workspace" && id != ":danger-full-access" {
			return nil, nil, Fail("Custom permission profile cannot be verified; use Desktop")
		}
		params["permissions"] = id
	} else {
		if context["file_system_sandbox_policy"] != nil {
			return nil, nil, Fail("Detailed filesystem policy cannot be restored; use Desktop")
		}
		params["sandbox"] = kind
	}
	if kind == "read-only" {
		expected["networkAccess"] = j.Default(sandbox, "network_access", false)
		if expected["networkAccess"] != false {
			return nil, nil, Fail("Read-only network override cannot be restored; use Desktop")
		}
	}
	if kind == "workspace-write" {
		cfg := j.Object{}
		for source, target := range map[string]string{"writable_roots": "writableRoots", "network_access": "networkAccess", "exclude_tmpdir_env_var": "excludeTmpdirEnvVar", "exclude_slash_tmp": "excludeSlashTmp"} {
			var def any = false
			if source == "writable_roots" {
				def = []any{}
			}
			v := j.Default(sandbox, source, def)
			cfg[source] = v
			expected[target] = v
		}
		config["sandbox_workspace_write"] = cfg
	}
	return params, expected, nil
}
func Verify(resumed, params, sandbox j.Object) error {
	checks := j.Object{"cwd": params["cwd"], "model": params["model"], "approvalPolicy": params["approvalPolicy"], "sandbox": sandbox, "reasoningEffort": j.Map(params["config"])["model_reasoning_effort"]}
	for _, key := range []string{"approvalsReviewer", "runtimeWorkspaceRoots", "modelProvider", "serviceTier"} {
		if v, ok := params[key]; ok {
			checks[key] = v
		}
	}
	for k, v := range checks {
		if !j.Equal(resumed[k], v) {
			return Fail("Resumed settings differ from saved settings; no message was sent")
		}
	}
	if v, ok := params["permissions"]; ok && !j.Equal(j.Map(resumed["activePermissionProfile"])["id"], v) {
		return Fail("Resumed permission profile differs; no message was sent")
	}
	return nil
}
