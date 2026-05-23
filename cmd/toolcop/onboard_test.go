package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeJSON(t *testing.T, path string, v any) {
	t.Helper()
	out, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(path, out, 0600); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func readJSON(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var s map[string]any
	if err := json.Unmarshal(data, &s); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, string(data))
	}
	return s
}

func TestOnboardCreatesSettings(t *testing.T) {
	dir := t.TempDir()
	settings := filepath.Join(dir, "settings.json")
	rules := filepath.Join(dir, "rules.yaml")

	if err := runOnboard(settings, rules, "/usr/local/bin/toolcop", false, false); err != nil {
		t.Fatalf("runOnboard: %v", err)
	}

	s := readJSON(t, settings)
	hooks, ok := s["hooks"].(map[string]any)
	if !ok {
		t.Fatalf("hooks missing: %+v", s)
	}
	pre, ok := hooks["PreToolUse"].([]any)
	if !ok || len(pre) != 1 {
		t.Fatalf("PreToolUse missing or wrong length: %+v", hooks)
	}
	entry := pre[0].(map[string]any)
	innerHooks := entry["hooks"].([]any)
	cmd := innerHooks[0].(map[string]any)["command"].(string)
	if cmd != "/usr/local/bin/toolcop" {
		t.Errorf("command = %q want /usr/local/bin/toolcop", cmd)
	}

	if _, err := os.Stat(rules); err != nil {
		t.Errorf("rules file not seeded: %v", err)
	}
}

func TestOnboardIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	settings := filepath.Join(dir, "settings.json")
	rules := filepath.Join(dir, "rules.yaml")

	for i := 0; i < 3; i++ {
		if err := runOnboard(settings, rules, "/usr/local/bin/toolcop", false, true); err != nil {
			t.Fatalf("runOnboard #%d: %v", i, err)
		}
	}
	s := readJSON(t, settings)
	pre := s["hooks"].(map[string]any)["PreToolUse"].([]any)
	if len(pre) != 1 {
		t.Errorf("expected exactly 1 PreToolUse entry after idempotent runs, got %d", len(pre))
	}
}

func TestOnboardUpdatesExistingPath(t *testing.T) {
	dir := t.TempDir()
	settings := filepath.Join(dir, "settings.json")
	rules := filepath.Join(dir, "rules.yaml")

	// Initial onboard
	if err := runOnboard(settings, rules, "/old/path/toolcop", false, true); err != nil {
		t.Fatalf("initial onboard: %v", err)
	}
	// Re-onboard with different path
	if err := runOnboard(settings, rules, "/new/path/toolcop", false, true); err != nil {
		t.Fatalf("second onboard: %v", err)
	}

	s := readJSON(t, settings)
	pre := s["hooks"].(map[string]any)["PreToolUse"].([]any)
	if len(pre) != 1 {
		t.Errorf("expected 1 entry after path update, got %d", len(pre))
	}
	cmd := pre[0].(map[string]any)["hooks"].([]any)[0].(map[string]any)["command"].(string)
	if cmd != "/new/path/toolcop" {
		t.Errorf("command not updated: %q", cmd)
	}
}

func TestOnboardPreservesUnrelatedSettings(t *testing.T) {
	dir := t.TempDir()
	settings := filepath.Join(dir, "settings.json")
	rules := filepath.Join(dir, "rules.yaml")

	original := map[string]any{
		"theme": "dark",
		"hooks": map[string]any{
			"UserPromptSubmit": []any{
				map[string]any{
					"matcher": ".*",
					"hooks": []any{
						map[string]any{"type": "command", "command": "/other/binary"},
					},
				},
			},
		},
	}
	writeJSON(t, settings, original)

	if err := runOnboard(settings, rules, "/usr/local/bin/toolcop", false, true); err != nil {
		t.Fatalf("runOnboard: %v", err)
	}

	s := readJSON(t, settings)
	if s["theme"] != "dark" {
		t.Errorf("theme lost: %+v", s)
	}
	hooks := s["hooks"].(map[string]any)
	if _, ok := hooks["UserPromptSubmit"]; !ok {
		t.Errorf("UserPromptSubmit hook lost")
	}
	if _, ok := hooks["PreToolUse"]; !ok {
		t.Errorf("PreToolUse not added")
	}
}

func TestOnboardCoexistsWithOtherPreToolUseHook(t *testing.T) {
	dir := t.TempDir()
	settings := filepath.Join(dir, "settings.json")
	rules := filepath.Join(dir, "rules.yaml")

	original := map[string]any{
		"hooks": map[string]any{
			"PreToolUse": []any{
				map[string]any{
					"matcher": "Bash",
					"hooks": []any{
						map[string]any{"type": "command", "command": "/some/other/gate"},
					},
				},
			},
		},
	}
	writeJSON(t, settings, original)

	if err := runOnboard(settings, rules, "/usr/local/bin/toolcop", false, true); err != nil {
		t.Fatalf("runOnboard: %v", err)
	}

	s := readJSON(t, settings)
	pre := s["hooks"].(map[string]any)["PreToolUse"].([]any)
	if len(pre) != 2 {
		t.Errorf("expected 2 entries (other + toolcop), got %d", len(pre))
	}
}

func TestOffboardRemovesOnlyToolcop(t *testing.T) {
	dir := t.TempDir()
	settings := filepath.Join(dir, "settings.json")

	original := map[string]any{
		"hooks": map[string]any{
			"PreToolUse": []any{
				map[string]any{
					"matcher": "Bash",
					"hooks": []any{
						map[string]any{"type": "command", "command": "/some/other/gate"},
					},
				},
				map[string]any{
					"matcher": ".*",
					"hooks": []any{
						map[string]any{"type": "command", "command": "/usr/local/bin/toolcop"},
					},
				},
			},
		},
	}
	writeJSON(t, settings, original)

	if err := runOffboard(settings, false, true); err != nil {
		t.Fatalf("runOffboard: %v", err)
	}

	s := readJSON(t, settings)
	pre := s["hooks"].(map[string]any)["PreToolUse"].([]any)
	if len(pre) != 1 {
		t.Errorf("expected 1 entry after offboard, got %d", len(pre))
	}
	cmd := pre[0].(map[string]any)["hooks"].([]any)[0].(map[string]any)["command"].(string)
	if cmd != "/some/other/gate" {
		t.Errorf("wrong entry kept: %q", cmd)
	}
}

func TestOffboardEmptyWhenNothingToRemove(t *testing.T) {
	dir := t.TempDir()
	settings := filepath.Join(dir, "settings.json")

	original := map[string]any{
		"hooks": map[string]any{
			"PreToolUse": []any{
				map[string]any{
					"matcher": "Bash",
					"hooks": []any{
						map[string]any{"type": "command", "command": "/some/other/gate"},
					},
				},
			},
		},
	}
	writeJSON(t, settings, original)
	rawBefore, _ := os.ReadFile(settings)

	if err := runOffboard(settings, false, true); err != nil {
		t.Fatalf("runOffboard: %v", err)
	}

	rawAfter, _ := os.ReadFile(settings)
	if string(rawBefore) != string(rawAfter) {
		t.Errorf("file should be unchanged when nothing to remove")
	}
}

func TestOnboardBackupCreated(t *testing.T) {
	dir := t.TempDir()
	settings := filepath.Join(dir, "settings.json")
	rules := filepath.Join(dir, "rules.yaml")

	writeJSON(t, settings, map[string]any{"existing": true})

	if err := runOnboard(settings, rules, "/usr/local/bin/toolcop", false, false); err != nil {
		t.Fatalf("runOnboard: %v", err)
	}

	entries, _ := os.ReadDir(dir)
	foundBackup := false
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "settings.json.toolcop-backup.") {
			foundBackup = true
			break
		}
	}
	if !foundBackup {
		t.Errorf("expected a backup file in %s", dir)
	}
}

func TestIsToolcopBinary(t *testing.T) {
	cases := map[string]bool{
		"/usr/local/bin/toolcop":          true,
		"/Users/foo/.local/bin/toolcop":   true,
		"toolcop":                         true,
		"/usr/local/bin/toolcop-extra":    false,
		"/usr/local/bin/some-other-tool":  false,
		"":                                false,
	}
	for in, want := range cases {
		if got := isToolcopBinary(in); got != want {
			t.Errorf("isToolcopBinary(%q) = %v want %v", in, got, want)
		}
	}
}
