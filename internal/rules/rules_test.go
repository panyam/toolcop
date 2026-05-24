package rules

import (
	"encoding/json"
	"testing"

	"github.com/panyam/toolcop/pkg/api"
)

func bashReq(cmd string) api.Request {
	input, _ := json.Marshal(api.BashInput{Command: cmd})
	return api.Request{
		SessionID:     "test",
		Cwd:           "/tmp",
		HookEventName: api.HookEventPreToolUse,
		ToolName:      "Bash",
		ToolInput:     input,
	}
}

func TestRuleMatchProgram(t *testing.T) {
	src := `
rules:
  - name: gh-allow
    match:
      tool: Bash
      program: gh
    decide: allow
    reason: gh ok
`
	eng, err := Parse([]byte(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	d := eng.Evaluate(bashReq("gh api foo"))
	if len(d) != 1 {
		t.Fatalf("expected 1 decision, got %d", len(d))
	}
	if d[0].Verdict != api.VerdictAllow {
		t.Errorf("verdict = %v want allow", d[0].Verdict)
	}
	if d[0].Source != "gh-allow" {
		t.Errorf("source = %q", d[0].Source)
	}
	if d[0].Reason != "gh ok" {
		t.Errorf("reason = %q", d[0].Reason)
	}
}

func TestRuleNoMatch(t *testing.T) {
	src := `
rules:
  - name: gh-allow
    match: { program: gh }
    decide: allow
`
	eng, _ := Parse([]byte(src))
	if d := eng.Evaluate(bashReq("ls")); len(d) != 0 {
		t.Errorf("expected no matches, got %d", len(d))
	}
}

func TestRuleProgramIn(t *testing.T) {
	src := `
rules:
  - name: vcs-status
    match:
      program_in: [git, hg, jj]
      subcommand: status
    decide: allow
`
	eng, _ := Parse([]byte(src))
	if d := eng.Evaluate(bashReq("git status")); len(d) != 1 {
		t.Errorf("git status: expected match, got %d", len(d))
	}
	if d := eng.Evaluate(bashReq("git diff")); len(d) != 0 {
		t.Errorf("git diff: expected no match, got %d", len(d))
	}
	if d := eng.Evaluate(bashReq("jj status")); len(d) != 1 {
		t.Errorf("jj status: expected match, got %d", len(d))
	}
}

func TestRuleArgsStartsWith(t *testing.T) {
	src := `
rules:
  - name: gh-pr-view
    match:
      program: gh
      args_starts_with: [pr, view]
    decide: allow
`
	eng, _ := Parse([]byte(src))
	if d := eng.Evaluate(bashReq("gh pr view 123")); len(d) != 1 {
		t.Errorf("gh pr view 123: expected match, got %d", len(d))
	}
	if d := eng.Evaluate(bashReq("gh pr create")); len(d) != 0 {
		t.Errorf("gh pr create: expected no match")
	}
	if d := eng.Evaluate(bashReq("gh pr")); len(d) != 0 {
		t.Errorf("gh pr (too short): expected no match")
	}
}

func TestRuleCommandRegexDeny(t *testing.T) {
	src := `
rules:
  - name: no-rm-rf-home
    match:
      command_regex: 'rm\s+-rf\s+(~|\$HOME)'
    decide: deny
    reason: refused
`
	eng, _ := Parse([]byte(src))
	d := eng.Evaluate(bashReq("rm -rf ~/foo"))
	if len(d) != 1 || d[0].Verdict != api.VerdictDeny {
		t.Errorf("rm -rf ~/foo: expected deny, got %v", d)
	}
	if d := eng.Evaluate(bashReq("rm foo")); len(d) != 0 {
		t.Errorf("rm foo: expected no match")
	}
}

func TestRuleEnvPrefixStripped(t *testing.T) {
	src := `
rules:
  - name: gh-allow
    match: { program: gh }
    decide: allow
`
	eng, _ := Parse([]byte(src))
	if d := eng.Evaluate(bashReq("GH_TOKEN=x gh api foo")); len(d) != 1 {
		t.Errorf("env-prefixed gh: expected match, got %d", len(d))
	}
}

func TestRuleWrapperStripped(t *testing.T) {
	src := `
rules:
  - name: npm-test
    match:
      program: npm
      subcommand: test
    decide: allow
`
	eng, _ := Parse([]byte(src))
	if d := eng.Evaluate(bashReq("timeout 30 npm test")); len(d) != 1 {
		t.Errorf("timeout-wrapped npm test: expected match, got %d", len(d))
	}
}

func TestRuleNonBashCantMatchBashFields(t *testing.T) {
	src := `
rules:
  - name: r1
    match: { program: gh }
    decide: allow
`
	eng, _ := Parse([]byte(src))
	req := api.Request{
		ToolName:  "Read",
		ToolInput: json.RawMessage(`{"file_path":"/etc/passwd"}`),
	}
	if d := eng.Evaluate(req); len(d) != 0 {
		t.Errorf("Read tool should not match program rule, got %d", len(d))
	}
}

func TestRuleMultipleMatchesCombineToAsk(t *testing.T) {
	src := `
rules:
  - name: r1
    match: { tool: Bash }
    decide: allow
    reason: r1
  - name: r2
    match: { program: gh }
    decide: ask
    reason: r2
`
	eng, _ := Parse([]byte(src))
	decisions := eng.Evaluate(bashReq("gh api foo"))
	if len(decisions) != 2 {
		t.Fatalf("expected 2 decisions, got %d", len(decisions))
	}
	final := api.Combine(decisions)
	if final.Verdict != api.VerdictAsk {
		t.Errorf("combined verdict = %v, want ask (ask beats allow)", final.Verdict)
	}
}

func TestRuleInvalidDecide(t *testing.T) {
	src := `
rules:
  - name: bad
    match: { tool: Bash }
    decide: maybe
`
	if _, err := Parse([]byte(src)); err == nil {
		t.Errorf("expected error for invalid decide")
	}
}

func TestRuleInvalidRegex(t *testing.T) {
	src := `
rules:
  - name: bad
    match:
      command_regex: '[unclosed'
    decide: deny
`
	if _, err := Parse([]byte(src)); err == nil {
		t.Errorf("expected error for invalid regex")
	}
}

func TestRuleEnvHasScalar(t *testing.T) {
	src := `
rules:
  - name: gh-with-token
    match:
      program: gh
      env_has: GH_TOKEN
    decide: allow
    reason: explicit token
`
	eng, err := Parse([]byte(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if d := eng.Evaluate(bashReq("GH_TOKEN=x gh api foo")); len(d) != 1 {
		t.Errorf("with GH_TOKEN: expected match, got %d", len(d))
	}
	if d := eng.Evaluate(bashReq("gh api foo")); len(d) != 0 {
		t.Errorf("without GH_TOKEN: expected no match, got %d", len(d))
	}
}

func TestRuleEnvHasSequence(t *testing.T) {
	src := `
rules:
  - name: needs-all
    match:
      tool: Bash
      env_has: [A, B]
    decide: allow
`
	eng, _ := Parse([]byte(src))
	if d := eng.Evaluate(bashReq("A=1 B=2 npm test")); len(d) != 1 {
		t.Errorf("A=1 B=2: expected match")
	}
	if d := eng.Evaluate(bashReq("A=1 npm test")); len(d) != 0 {
		t.Errorf("only A set: expected no match (AND semantics)")
	}
}

func TestRuleEnvEquals(t *testing.T) {
	src := `
rules:
  - name: npm-test-only
    match:
      program: npm
      subcommand: test
      env_equals:
        NODE_ENV: test
    decide: allow
`
	eng, _ := Parse([]byte(src))
	if d := eng.Evaluate(bashReq("NODE_ENV=test npm test")); len(d) != 1 {
		t.Errorf("NODE_ENV=test: expected match")
	}
	if d := eng.Evaluate(bashReq("NODE_ENV=production npm test")); len(d) != 0 {
		t.Errorf("NODE_ENV=production: expected no match")
	}
	if d := eng.Evaluate(bashReq("npm test")); len(d) != 0 {
		t.Errorf("no NODE_ENV: expected no match")
	}
}

// --- Compound-command safety tests ---

const safetyRules = `
rules:
  - name: git-readonly
    match:
      program: git
      subcommand_in: [status, log, diff, show, blame]
    decide: allow
    reason: git-readonly

  - name: never-rm-rf-home
    match:
      command_regex: 'rm\s+-rf?\s+(~|\$HOME)'
    decide: deny
    reason: refused
`

func TestCompoundDenyOnRhsChain(t *testing.T) {
	// Critical safety case: an allow on the LHS must not mask a deny on
	// the RHS of a chain. (This was the bug before the AST-walk fix.)
	eng, err := Parse([]byte(safetyRules))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	final := api.Combine(eng.Evaluate(bashReq("git status && rm -rf ~/foo")))
	if final.Verdict != api.VerdictDeny {
		t.Errorf("git status && rm -rf ~/foo: combined verdict = %v, want deny", final.Verdict)
	}
}

func TestCompoundAllAllowedChain(t *testing.T) {
	eng, _ := Parse([]byte(safetyRules))
	final := api.Combine(eng.Evaluate(bashReq("git status && git diff")))
	if final.Verdict != api.VerdictAllow {
		t.Errorf("git status && git diff: combined verdict = %v, want allow", final.Verdict)
	}
}

func TestCompoundUnmatchedSegmentForcesAsk(t *testing.T) {
	// `git status` matches the allow rule, but `something-unknown` has
	// no matching rule — the compound must NOT silently allow.
	eng, _ := Parse([]byte(safetyRules))
	final := api.Combine(eng.Evaluate(bashReq("git status && something-unknown-here")))
	if final.Verdict != api.VerdictAsk {
		t.Errorf("git status && unknown: verdict = %v, want ask (an unmatched compound segment must force a prompt)", final.Verdict)
	}
}

func TestCompoundSemicolonChainEvalsBoth(t *testing.T) {
	eng, _ := Parse([]byte(safetyRules))
	final := api.Combine(eng.Evaluate(bashReq("git status; rm -rf ~/important")))
	if final.Verdict != api.VerdictDeny {
		t.Errorf("semicolon-chain: verdict = %v, want deny", final.Verdict)
	}
}

func TestCompoundOrChainEvalsBoth(t *testing.T) {
	eng, _ := Parse([]byte(safetyRules))
	// `||` means RHS only runs if LHS fails, but it CAN run — must be vetted.
	final := api.Combine(eng.Evaluate(bashReq("git status || rm -rf ~/foo")))
	if final.Verdict != api.VerdictDeny {
		t.Errorf("or-chain: verdict = %v, want deny", final.Verdict)
	}
}

func TestCompoundForLoopBodyVetted(t *testing.T) {
	eng, _ := Parse([]byte(safetyRules))
	final := api.Combine(eng.Evaluate(bashReq("for f in *.go; do rm -rf ~/foo; done")))
	if final.Verdict != api.VerdictDeny {
		t.Errorf("for-loop body: verdict = %v, want deny", final.Verdict)
	}
}

func TestCompoundCommandSubstitutionVetted(t *testing.T) {
	eng, _ := Parse([]byte(safetyRules))
	// rm hidden in $(...) must still be caught.
	final := api.Combine(eng.Evaluate(bashReq(`echo "$(rm -rf ~/foo)"`)))
	if final.Verdict != api.VerdictDeny {
		t.Errorf("$() body: verdict = %v, want deny", final.Verdict)
	}
}

func TestCompoundSubshellVetted(t *testing.T) {
	eng, _ := Parse([]byte(safetyRules))
	final := api.Combine(eng.Evaluate(bashReq("(cd /tmp && rm -rf ~/foo)")))
	if final.Verdict != api.VerdictDeny {
		t.Errorf("subshell: verdict = %v, want deny", final.Verdict)
	}
}

func TestSimpleUnmatchedStillFallsToAsk(t *testing.T) {
	// Make sure the new compound safety logic doesn't break the
	// single-command "no match → ask" behavior.
	eng, _ := Parse([]byte(safetyRules))
	decisions := eng.Evaluate(bashReq("some-random-tool --foo"))
	if len(decisions) != 0 {
		t.Errorf("simple unmatched: expected no decisions (legacy behavior), got %d: %+v", len(decisions), decisions)
	}
	// Combine to make sure the wire verdict is still ask.
	if final := api.Combine(decisions); final.Verdict != api.VerdictPass {
		t.Errorf("simple unmatched: combined = %v, want pass (renders as ask)", final.Verdict)
	}
}

func TestLoadMissingFileReturnsEmpty(t *testing.T) {
	eng, err := Load("/nonexistent/path/rules.yaml")
	if err != nil {
		t.Fatalf("missing file should not error: %v", err)
	}
	if len(eng.Rules) != 0 {
		t.Errorf("expected empty engine, got %d rules", len(eng.Rules))
	}
	// An empty engine should evaluate to no decisions → caller folds to Pass.
	if d := eng.Evaluate(bashReq("anything")); len(d) != 0 {
		t.Errorf("empty engine returned %d decisions", len(d))
	}
}
