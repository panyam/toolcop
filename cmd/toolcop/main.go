// toolcop — context-aware permission gate for AI-agent tool calls.
//
// Single binary; cobra dispatches to client or daemon. With no subcommand
// and a non-tty stdin, runs as the PreToolUse hook client — the hot path
// short-circuits cobra to keep cold-start tight.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/panyam/toolcop/internal/client"
	"github.com/panyam/toolcop/internal/daemon"
	"github.com/panyam/toolcop/internal/parse"
	"github.com/panyam/toolcop/internal/rules"
	"github.com/panyam/toolcop/pkg/api"
	"github.com/spf13/cobra"
)

const version = "v0.0.0-dev"

func main() {
	// Hot path: no subcommand + piped stdin = hook client. Skip building
	// the cobra command tree — keeps the hook contract simple and avoids
	// any chance of cobra output leaking onto stdout (which the agent
	// parses as JSON).
	if len(os.Args) == 1 && !isStdinTerm() {
		runClient()
		return
	}
	if err := newRootCmd().Execute(); err != nil {
		os.Exit(1)
	}
}

func newRootCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "toolcop",
		Short: "Context-aware permission gate for AI-agent tool calls",
		Long: `toolcop is a PreToolUse hook + daemon for AI coding agents.

Replaces flaky prefix-based command matchers in agent settings with
programmable rules (declarative YAML now, stateful Go modules later).

V1 ships with Claude Code support. The wire format and adapter layer
are designed to extend to other agents (Cursor, etc.) without changes
to the daemon, rules, or modules.`,
		Version:       version,
		SilenceUsage:  true,
		SilenceErrors: true,
		Run: func(cmd *cobra.Command, args []string) {
			if isStdinTerm() {
				_ = cmd.Help()
				return
			}
			// stdin piped but cobra was invoked anyway (unusual). Treat
			// as hook client.
			runClient()
		},
	}
	cmd.AddCommand(newDaemonCmd(), newTestCmd(), newStatusCmd(), newOnboardCmd(), newOffboardCmd())
	return cmd
}

func newDaemonCmd() *cobra.Command {
	var (
		socket    string
		rulesPath string
		shadow    bool
	)
	cmd := &cobra.Command{
		Use:   "daemon",
		Short: "Run the toolcop daemon",
		Long: `Runs the long-lived daemon that evaluates hook requests from clients.
Listens on a unix socket; loads YAML rules at startup. SIGTERM / SIGINT
trigger a graceful shutdown.

--shadow runs in pass-through mode: rules are evaluated and logged, but
the wire response is always 'ask' so the agent prompts as if toolcop
weren't there. Use this to pilot a rule set against real sessions and
inspect daemon.log to see what would have changed. WARNING: deny rules
are inert in shadow mode — don't rely on them for safety while
shadowing.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			eng, err := rules.Load(rulesPath)
			if err != nil {
				return fmt.Errorf("load rules: %w", err)
			}
			mode := "live"
			if shadow {
				mode = "SHADOW (pass-through; denies inert)"
			}
			log.Printf("toolcop daemon %s: loaded %d rules from %s [mode=%s]",
				version, len(eng.Rules), rulesPath, mode)

			d := daemon.New(socket, eng, nil)
			d.Shadow = shadow
			if err := d.Listen(); err != nil {
				return fmt.Errorf("listen: %w", err)
			}

			sigs := make(chan os.Signal, 1)
			signal.Notify(sigs, syscall.SIGTERM, syscall.SIGINT)
			go func() {
				sig := <-sigs
				log.Printf("received %s, shutting down", sig)
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				_ = d.Shutdown(ctx)
			}()

			return d.Serve()
		},
	}
	cmd.Flags().StringVar(&socket, "socket", daemon.DefaultSocketPath(), "unix socket path")
	cmd.Flags().StringVar(&rulesPath, "rules", defaultRulesPath(), "YAML rules file")
	cmd.Flags().BoolVar(&shadow, "shadow", false, "pass-through mode: log would-be decisions but always emit `ask`")
	return cmd
}

func newTestCmd() *cobra.Command {
	var rulesPath string
	cmd := &cobra.Command{
		Use:   "test <command>",
		Short: "Dry-run a Bash command through the rule engine",
		Long: `Parses <command>, evaluates the loaded rules against it, and prints the
matching rules, the combined verdict, and the wire response that would be
sent to the agent. No daemon required.`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runTestImpl(strings.Join(args, " "), rulesPath)
		},
	}
	cmd.Flags().StringVar(&rulesPath, "rules", defaultRulesPath(), "YAML rules file")
	return cmd
}

func newStatusCmd() *cobra.Command {
	var socket string
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Check whether the daemon is reachable",
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Printf("Socket: %s\n", socket)
			if _, err := os.Stat(socket); os.IsNotExist(err) {
				fmt.Println("Status: not running (no socket)")
				return fmt.Errorf("daemon not running")
			}
			c, err := net.DialTimeout("unix", socket, 200*time.Millisecond)
			if err != nil {
				fmt.Printf("Status: socket exists but not reachable: %v\n", err)
				return err
			}
			c.Close()
			fmt.Println("Status: running")
			return nil
		},
	}
	cmd.Flags().StringVar(&socket, "socket", daemon.DefaultSocketPath(), "unix socket path")
	return cmd
}

func runClient() {
	cfg := client.Config{
		SocketPath: daemon.DefaultSocketPath(),
		AutoFork:   true,
	}
	if err := client.Run(cfg); err != nil {
		fmt.Fprintf(os.Stderr, "toolcop: client write error: %v\n", err)
		os.Exit(1)
	}
}

func runTestImpl(cmd, rulesPath string) error {
	eng, err := rules.Load(rulesPath)
	if err != nil {
		return fmt.Errorf("load rules: %w", err)
	}

	parsedSet, perr := parse.ParseBash(cmd)
	fmt.Printf("Command: %s\n", cmd)
	if perr != nil {
		fmt.Printf("Parse error: %v\n", perr)
	} else if len(parsedSet.Commands) == 0 {
		fmt.Println("  (no command invocations extracted)")
	} else {
		if parsedSet.IsCompound() {
			fmt.Printf("  Compound: %d segments\n", len(parsedSet.Commands))
		}
		for i, c := range parsedSet.Commands {
			prefix := "  "
			if parsedSet.IsCompound() {
				prefix = fmt.Sprintf("  Cmd[%d]", i)
			}
			fmt.Printf("%s Program:    %s\n", prefix, c.Program)
			fmt.Printf("%s Subcommand: %s\n", prefix, c.Subcommand)
			fmt.Printf("%s Args:       %v\n", prefix, c.Args)
			if len(c.Env) > 0 {
				fmt.Printf("%s Env:        %v\n", prefix, c.Env)
			}
		}
	}
	fmt.Println()

	toolInput, _ := json.Marshal(api.BashInput{Command: cmd})
	cwd, _ := os.Getwd()
	req := api.Request{
		SessionID:     "toolcop-test",
		Cwd:           cwd,
		HookEventName: api.HookEventPreToolUse,
		ToolName:      "Bash",
		ToolInput:     toolInput,
	}

	decisions := eng.Evaluate(req)
	fmt.Printf("Rules matched (%d):\n", len(decisions))
	if len(decisions) == 0 {
		fmt.Println("  (none — falls through to ask)")
	}
	for _, d := range decisions {
		fmt.Printf("  - %-24s [%s] %s\n", d.Source, d.Verdict, d.Reason)
	}
	fmt.Println()

	final := api.Combine(decisions)
	fmt.Printf("Final verdict: %s\n", final.Verdict)
	if final.Reason != "" {
		fmt.Printf("Reason: %s\n", final.Reason)
	}
	fmt.Println()

	fmt.Println("Wire response:")
	resp := final.ToResponse()
	enc, _ := json.MarshalIndent(resp, "  ", "  ")
	fmt.Printf("  %s\n", string(enc))
	return nil
}

func defaultRulesPath() string {
	if dir := os.Getenv("XDG_CONFIG_HOME"); dir != "" {
		return filepath.Join(dir, "toolcop", "rules.yaml")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "toolcop", "rules.yaml")
}

func isStdinTerm() bool {
	fi, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return (fi.Mode() & os.ModeCharDevice) != 0
}
