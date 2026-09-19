package settings

import (
	"encoding/json"
	j "github.com/JesseSenior/codex-thread-bridge/internal/jsonutil"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func metadata(t *testing.T, fields j.Object) j.Object {
	t.Helper()
	dir := t.TempDir()
	context := j.Object{"cwd": dir, "model": "model", "effort": "low", "approval_policy": "on-request", "sandbox_policy": j.Object{"type": "read-only"}, "workspace_roots": []any{dir}}
	for k, v := range fields {
		context[k] = v
	}
	path := filepath.Join(dir, "task.jsonl")
	a, _ := json.Marshal(j.Object{"type": "session_meta", "payload": j.Object{"id": "task"}})
	b, _ := json.Marshal(j.Object{"type": "turn_context", "payload": context})
	if e := os.WriteFile(path, []byte(string(a)+"\n"+string(b)+"\n"), 0600); e != nil {
		t.Fatal(e)
	}
	return j.Object{"id": "task", "cwd": dir, "path": path}
}
func TestSavedSandboxFields(t *testing.T) {
	for _, kind := range []string{"read-only", "danger-full-access", "workspace-write"} {
		t.Run(kind, func(t *testing.T) {
			policy := j.Object{"type": kind}
			expected := j.Object{"type": "dangerFullAccess"}
			if kind == "read-only" {
				expected = j.Object{"type": "readOnly", "networkAccess": false}
			}
			if kind == "workspace-write" {
				policy = j.Object{"type": kind, "writable_roots": []any{"/extra"}, "network_access": true, "exclude_slash_tmp": true, "exclude_tmpdir_env_var": true}
				expected = j.Object{"type": "workspaceWrite", "writableRoots": []any{"/extra"}, "networkAccess": true, "excludeSlashTmp": true, "excludeTmpdirEnvVar": true}
			}
			thread := metadata(t, j.Object{"sandbox_policy": policy})
			p, s, e := Saved(thread)
			if e != nil {
				t.Fatal(e)
			}
			if !j.Equal(s, expected) || p["sandbox"] != kind {
				t.Fatalf("%v %v", p, s)
			}
			response := j.Object{"cwd": p["cwd"], "model": "model", "reasoningEffort": "low", "approvalPolicy": "on-request", "sandbox": s, "runtimeWorkspaceRoots": []any{thread["cwd"]}}
			if e = Verify(response, p, s); e != nil {
				t.Fatal(e)
			}
			response["runtimeWorkspaceRoots"] = []any{"/different"}
			if Verify(response, p, s) == nil {
				t.Fatal("accepted mismatch")
			}
		})
	}
}
func TestBuiltinAndCustomProfile(t *testing.T) {
	thread := metadata(t, j.Object{"active_permission_profile": j.Object{"id": ":read-only"}})
	p, _, e := Saved(thread)
	if e != nil || p["permissions"] != ":read-only" || p["sandbox"] != nil {
		t.Fatalf("%v %v", p, e)
	}
	thread = metadata(t, j.Object{"active_permission_profile": j.Object{"id": "custom"}})
	if _, _, e = Saved(thread); e == nil || !strings.Contains(e.Error(), "Custom permission profile") {
		t.Fatal(e)
	}
}
func TestMissingWrongOwnerAndIncompleteMetadata(t *testing.T) {
	if _, _, e := Saved(j.Object{"id": "task"}); e == nil {
		t.Fatal("missing accepted")
	}
	thread := metadata(t, nil)
	thread["id"] = "other"
	if _, _, e := Saved(thread); e == nil {
		t.Fatal("wrong owner")
	}
	thread = metadata(t, nil)
	path := j.String(thread["path"])
	f, e := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
	if e != nil {
		t.Fatal(e)
	}
	defer f.Close()
	_, _ = f.WriteString(`{"type": "turn_context"`)
	p, _, e := Saved(thread)
	if e != nil || p["model"] != "model" {
		t.Fatalf("%v %v", p, e)
	}
	_, _ = f.WriteString("\n")
	if _, _, e = Saved(thread); e == nil || !strings.Contains(e.Error(), "Invalid task metadata") {
		t.Fatal(e)
	}
}
func TestUnsupportedPolicies(t *testing.T) {
	for _, fields := range []j.Object{{"sandbox_policy": j.Object{"type": "read-only", "network_access": true}}, {"file_system_sandbox_policy": j.Object{}}, {"sandbox_policy": j.Object{"type": "external"}}} {
		thread := metadata(t, fields)
		if _, _, e := Saved(thread); e == nil {
			t.Fatal("unsafe policy accepted")
		}
	}
}
