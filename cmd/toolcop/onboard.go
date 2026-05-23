package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"
)

// starterRulesYAML is dropped into the user's rules.yaml on first onboard
// when no rules file exists. Kept minimal on purpose — examples/rules.yaml
// in the repo is the richer reference set.
const starterRulesYAML = `# toolcop starter rules — see examples/rules.yaml in the repo for more.
rules:
  - name: gh-readonly
    match:
      tool: Bash
      program: gh
      args_starts_with: [api]
    decide: allow
    reason: gh read-only

  - name: git-readonly
    match:
      tool: Bash
      program: git
      subcommand_in: [status, log, diff, show, blame, branch]
    decide: allow
    reason: git read-only

  - name: never-rm-rf-home
    match:
      tool: Bash
      command_regex: 'rm\s+-rf?\s+(~|\$HOME)'
    decide: deny
    reason: refused — rm -rf of home directory
`

func newOnboardCmd() *cobra.Command {
	var (
		settingsPath string
		rulesPath    string
		binPath      string
		dryRun       bool
		noBackup     bool
	)
	cmd := &cobra.Command{
		Use:   "onboard",
		Short: "Wire toolcop into the agent's settings (Claude Code)",
		Long: `Adds toolcop as a PreToolUse hook in the agent's settings and seeds a
starter rules file if one doesn't exist yet.

Idempotent: re-running with the same binary path is a no-op; re-running
with a different binary path updates the existing entry. By default, the
existing settings file is backed up before any change.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if binPath == "" {
				exe, err := os.Executable()
				if err != nil {
					return fmt.Errorf("resolve own path: %w", err)
				}
				binPath = exe
			}
			return runOnboard(settingsPath, rulesPath, binPath, dryRun, noBackup)
		},
	}
	cmd.Flags().StringVar(&settingsPath, "settings", defaultClaudeSettingsPath(), "agent settings.json path")
	cmd.Flags().StringVar(&rulesPath, "rules", defaultRulesPath(), "rules.yaml path to seed if missing")
	cmd.Flags().StringVar(&binPath, "bin", "", "absolute path to the toolcop binary to register (default: own path)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "print the planned changes without writing")
	cmd.Flags().BoolVar(&noBackup, "no-backup", false, "skip the timestamped backup of settings.json")
	return cmd
}

func newOffboardCmd() *cobra.Command {
	var (
		settingsPath string
		dryRun       bool
		noBackup     bool
	)
	cmd := &cobra.Command{
		Use:   "offboard",
		Short: "Remove toolcop's hook from the agent's settings",
		Long: `Removes any PreToolUse hook entry whose command resolves to a toolcop binary.
The rules file and state directory are left in place — delete them by hand
if you want a clean uninstall.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runOffboard(settingsPath, dryRun, noBackup)
		},
	}
	cmd.Flags().StringVar(&settingsPath, "settings", defaultClaudeSettingsPath(), "agent settings.json path")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "print the planned changes without writing")
	cmd.Flags().BoolVar(&noBackup, "no-backup", false, "skip the timestamped backup of settings.json")
	return cmd
}

func runOnboard(settingsPath, rulesPath, binPath string, dryRun, noBackup bool) error {
	abs, err := filepath.Abs(binPath)
	if err != nil {
		return fmt.Errorf("resolve binary path: %w", err)
	}
	binPath = abs

	settings, rawOriginal, err := loadSettings(settingsPath)
	if err != nil {
		return err
	}

	changed, action := upsertToolcopHook(settings, binPath)

	rulesAction := "rules file already exists"
	rulesWillWrite := false
	if _, statErr := os.Stat(rulesPath); errors.Is(statErr, fs.ErrNotExist) {
		rulesWillWrite = true
		rulesAction = fmt.Sprintf("seed starter rules at %s", rulesPath)
	}

	fmt.Printf("Settings: %s\n", settingsPath)
	fmt.Printf("Binary:   %s\n", binPath)
	fmt.Printf("Rules:    %s (%s)\n", rulesPath, rulesAction)
	fmt.Printf("Hook:     %s\n", action)

	if dryRun {
		if changed {
			fmt.Println("\n--- dry-run: settings.json after change ---")
			out, _ := json.MarshalIndent(settings, "", "  ")
			fmt.Println(string(out))
		}
		return nil
	}

	if changed {
		if !noBackup && rawOriginal != nil {
			backup := backupPath(settingsPath)
			if err := os.WriteFile(backup, rawOriginal, 0600); err != nil {
				return fmt.Errorf("backup: %w", err)
			}
			fmt.Printf("Backup:   %s\n", backup)
		}
		if err := writeSettings(settingsPath, settings); err != nil {
			return err
		}
		fmt.Printf("Written:  %s\n", settingsPath)
	}

	if rulesWillWrite {
		if err := os.MkdirAll(filepath.Dir(rulesPath), 0700); err != nil {
			return fmt.Errorf("mkdir rules dir: %w", err)
		}
		if err := os.WriteFile(rulesPath, []byte(starterRulesYAML), 0644); err != nil {
			return fmt.Errorf("write rules: %w", err)
		}
		fmt.Printf("Seeded:   %s\n", rulesPath)
	}

	if !changed && !rulesWillWrite {
		fmt.Println("\nNothing to do — already onboarded.")
	} else {
		fmt.Println("\nDone. Start a fresh agent session for the hook to take effect.")
	}
	return nil
}

func runOffboard(settingsPath string, dryRun, noBackup bool) error {
	settings, rawOriginal, err := loadSettings(settingsPath)
	if err != nil {
		return err
	}
	if settings == nil {
		fmt.Println("No settings file — nothing to do.")
		return nil
	}

	removed := removeToolcopHook(settings)
	if removed == 0 {
		fmt.Println("No toolcop hook entries found — nothing to do.")
		return nil
	}
	fmt.Printf("Removed %d hook entr%s pointing to a toolcop binary.\n", removed, plural(removed, "y", "ies"))

	if dryRun {
		fmt.Println("\n--- dry-run: settings.json after change ---")
		out, _ := json.MarshalIndent(settings, "", "  ")
		fmt.Println(string(out))
		return nil
	}

	if !noBackup && rawOriginal != nil {
		backup := backupPath(settingsPath)
		if err := os.WriteFile(backup, rawOriginal, 0600); err != nil {
			return fmt.Errorf("backup: %w", err)
		}
		fmt.Printf("Backup:   %s\n", backup)
	}
	if err := writeSettings(settingsPath, settings); err != nil {
		return err
	}
	fmt.Printf("Written:  %s\n", settingsPath)
	return nil
}

func loadSettings(path string) (map[string]any, []byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return map[string]any{}, nil, nil
		}
		return nil, nil, fmt.Errorf("read %s: %w", path, err)
	}
	var s map[string]any
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, nil, fmt.Errorf("parse %s: %w\n(toolcop requires strict JSON; if your settings have comments or trailing commas, fix those by hand first)", path, err)
	}
	if s == nil {
		s = map[string]any{}
	}
	return s, data, nil
}

func writeSettings(path string, s map[string]any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return fmt.Errorf("mkdir settings dir: %w", err)
	}
	out, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal settings: %w", err)
	}
	out = append(out, '\n')
	return os.WriteFile(path, out, 0600)
}

// upsertToolcopHook either updates an existing toolcop entry's command (if
// found) or appends a new entry. Returns whether anything changed and a
// human-readable description.
func upsertToolcopHook(settings map[string]any, command string) (changed bool, action string) {
	hooks, ok := settings["hooks"].(map[string]any)
	if !ok {
		hooks = map[string]any{}
		settings["hooks"] = hooks
	}
	preToolUse, _ := hooks["PreToolUse"].([]any)

	for _, item := range preToolUse {
		entry, ok := item.(map[string]any)
		if !ok {
			continue
		}
		innerHooks, _ := entry["hooks"].([]any)
		for _, h := range innerHooks {
			hookMap, ok := h.(map[string]any)
			if !ok {
				continue
			}
			existing, _ := hookMap["command"].(string)
			if isToolcopBinary(existing) {
				if existing == command {
					return false, "already present, no change"
				}
				hookMap["command"] = command
				return true, fmt.Sprintf("updated existing entry: %s → %s", existing, command)
			}
		}
	}

	newEntry := map[string]any{
		"matcher": ".*",
		"hooks": []any{
			map[string]any{
				"type":    "command",
				"command": command,
			},
		},
	}
	hooks["PreToolUse"] = append(preToolUse, newEntry)
	return true, fmt.Sprintf("added new PreToolUse entry → %s", command)
}

// removeToolcopHook strips out any inner hook whose command resolves to a
// toolcop binary, and prunes empty parent entries. Returns count removed.
func removeToolcopHook(settings map[string]any) int {
	hooks, ok := settings["hooks"].(map[string]any)
	if !ok {
		return 0
	}
	preToolUse, _ := hooks["PreToolUse"].([]any)
	if len(preToolUse) == 0 {
		return 0
	}

	removed := 0
	var keptEntries []any
	for _, item := range preToolUse {
		entry, ok := item.(map[string]any)
		if !ok {
			keptEntries = append(keptEntries, item)
			continue
		}
		innerHooks, _ := entry["hooks"].([]any)
		var keptInner []any
		for _, h := range innerHooks {
			hookMap, ok := h.(map[string]any)
			if !ok {
				keptInner = append(keptInner, h)
				continue
			}
			cmd, _ := hookMap["command"].(string)
			if isToolcopBinary(cmd) {
				removed++
				continue
			}
			keptInner = append(keptInner, h)
		}
		if len(keptInner) == 0 {
			// Whole entry pruned.
			continue
		}
		entry["hooks"] = keptInner
		keptEntries = append(keptEntries, entry)
	}

	if len(keptEntries) == 0 {
		delete(hooks, "PreToolUse")
		if len(hooks) == 0 {
			delete(settings, "hooks")
		}
	} else {
		hooks["PreToolUse"] = keptEntries
	}
	return removed
}

func isToolcopBinary(cmd string) bool {
	if cmd == "" {
		return false
	}
	return filepath.Base(cmd) == "toolcop"
}

func defaultClaudeSettingsPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".claude", "settings.json")
}

func backupPath(path string) string {
	return fmt.Sprintf("%s.toolcop-backup.%s", path, time.Now().Format("2006-01-02T15-04-05"))
}

func plural(n int, singular, pluralForm string) string {
	if n == 1 {
		return singular
	}
	return pluralForm
}
