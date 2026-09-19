package bridge

import (
	"regexp"
	"strconv"
	"strings"

	j "github.com/JesseSenior/codex-thread-bridge/internal/jsonutil"
)

var delegationEscape = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")
var delegationUnescape = strings.NewReplacer("&lt;", "<", "&gt;", ">", "&amp;", "&")
var delegationPattern = regexp.MustCompile(`^<codex_delegation>\s*<source_thread_id>([^<]*)</source_thread_id>\s*<input>([^<]*)</input>\s*</codex_delegation>$`)

func delegationText(source, prompt string) string {
	return "<codex_delegation>\n  <source_thread_id>" + delegationEscape.Replace(source) + "</source_thread_id>\n  <input>" + delegationEscape.Replace(prompt) + "</input>\n</codex_delegation>"
}

func parseDelegation(text string) (source, prompt string, ok bool) {
	match := delegationPattern.FindStringSubmatch(strings.TrimSpace(text))
	if match == nil {
		return "", "", false
	}
	source = delegationUnescape.Replace(match[1])
	if nonempty(source, "sourceThreadId", 128) != nil {
		return "", "", false
	}
	return source, delegationUnescape.Replace(match[2]), true
}

// Accept a semantic version, optionally prefixed by the server product name.
var serverVersionPattern = regexp.MustCompile(`^(?:[A-Za-z][A-Za-z0-9_-]*/)?(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-([0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*))?(?:\+[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?(?:\s.*)?$`)

func supportsToolOutput(userAgent string) bool {
	m := serverVersionPattern.FindStringSubmatch(userAgent)
	if m == nil {
		return false
	}
	parts := [3]int{}
	for i := range parts {
		n, err := strconv.Atoi(m[i+1])
		if err != nil {
			return false
		}
		parts[i] = n
	}
	pre := strings.Split(m[4], ".")
	for _, part := range pre {
		if len(part) > 1 && part[0] == '0' && strings.Trim(part, "0123456789") == "" {
			return false
		}
	}
	minimum := [3]int{0, 151, 0}
	for i, n := range parts {
		if n != minimum[i] {
			return n > minimum[i]
		}
	}
	if m[4] == "" {
		return true
	}
	// At the boundary, compare the prerelease against alpha.4.
	if pre[0] != "alpha" {
		return strings.Trim(pre[0], "0123456789") != "" && pre[0] > "alpha"
	}
	if len(pre) < 2 {
		return false
	}
	if strings.Trim(pre[1], "0123456789") != "" {
		return true
	}
	n, err := strconv.Atoi(pre[1])
	return err == nil && n >= 4
}

func (b *Bridge) messageInput(prompt, source, tool string, steering bool) j.Object {
	if source == "" {
		return j.Object{"input": input(prompt)}
	}
	wrapped := delegationText(source, prompt)
	if !steering && supportsToolOutput(j.String(b.RPC.Info()["userAgent"])) {
		return j.Object{"input": []any{}, "toolOutput": j.Object{"namespace": "codex_app", "name": tool, "output": wrapped}}
	}
	return j.Object{"input": input(wrapped)}
}
