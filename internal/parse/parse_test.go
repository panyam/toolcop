package parse

import (
	"reflect"
	"testing"
)

// primary parses cmd and returns the first Command. Fails the test if
// parsing fails or no commands are extracted.
func primary(t *testing.T, cmd string) *Command {
	t.Helper()
	s, err := ParseBash(cmd)
	if err != nil {
		t.Fatalf("parse %q: %v", cmd, err)
	}
	p := s.Primary()
	if p == nil {
		t.Fatalf("no commands parsed from %q", cmd)
	}
	return p
}

// parsed parses cmd and returns the full set.
func parsed(t *testing.T, cmd string) *CommandSet {
	t.Helper()
	s, err := ParseBash(cmd)
	if err != nil {
		t.Fatalf("parse %q: %v", cmd, err)
	}
	return s
}

func TestParseSimple(t *testing.T) {
	p := primary(t, "ls -la")
	if p.Program != "ls" {
		t.Errorf("Program = %q want ls", p.Program)
	}
	if !reflect.DeepEqual(p.Args, []string{"-la"}) {
		t.Errorf("Args = %v want [-la]", p.Args)
	}
}

func TestParseEnvPrefix(t *testing.T) {
	p := primary(t, "A=1 B=hello gh api foo/bar")
	if p.Env["A"] != "1" {
		t.Errorf("Env[A] = %q want 1", p.Env["A"])
	}
	if p.Env["B"] != "hello" {
		t.Errorf("Env[B] = %q want hello", p.Env["B"])
	}
	if p.Program != "gh" || p.Subcommand != "api" {
		t.Errorf("Program/Subcommand = %q/%q want gh/api", p.Program, p.Subcommand)
	}
	if !reflect.DeepEqual(p.Args, []string{"api", "foo/bar"}) {
		t.Errorf("Args = %v want [api foo/bar]", p.Args)
	}
}

func TestParseWrapperTimeout(t *testing.T) {
	p := primary(t, "timeout 30 npm test")
	if p.Program != "npm" || p.Subcommand != "test" {
		t.Errorf("Program/Subcommand = %q/%q want npm/test", p.Program, p.Subcommand)
	}
}

func TestParseWrapperTimeoutWithFlags(t *testing.T) {
	p := primary(t, "timeout -k 5s 30s npm test")
	if p.Program != "npm" {
		t.Errorf("Program = %q want npm", p.Program)
	}
}

func TestParseWrapperNice(t *testing.T) {
	p := primary(t, "nice -n 10 make build")
	if p.Program != "make" {
		t.Errorf("Program = %q want make", p.Program)
	}
}

func TestParseEnvCommand(t *testing.T) {
	p := primary(t, "env A=1 B=2 npm test")
	if p.Program != "npm" {
		t.Errorf("Program = %q want npm", p.Program)
	}
}

func TestParseQuotedArgs(t *testing.T) {
	p := primary(t, `gh issue create --title "hello world"`)
	if p.Program != "gh" {
		t.Errorf("Program = %q", p.Program)
	}
	want := []string{"issue", "create", "--title", "hello world"}
	if !reflect.DeepEqual(p.Args, want) {
		t.Errorf("Args = %v want %v", p.Args, want)
	}
}

func TestParseEmpty(t *testing.T) {
	s, err := ParseBash("")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(s.Commands) != 0 {
		t.Errorf("expected 0 commands, got %d", len(s.Commands))
	}
}

func TestParseSyntaxError(t *testing.T) {
	_, err := ParseBash(`echo "unterminated`)
	if err == nil {
		t.Errorf("expected error on unterminated quote")
	}
}

// --- Compound command tests (the safety-critical ones) ---

func TestParsePipelineCollectsBoth(t *testing.T) {
	s := parsed(t, "ls -la | grep foo")
	if len(s.Commands) != 2 {
		t.Fatalf("pipeline: expected 2 commands, got %d", len(s.Commands))
	}
	if s.Commands[0].Program != "ls" {
		t.Errorf("first = %q want ls", s.Commands[0].Program)
	}
	if s.Commands[1].Program != "grep" {
		t.Errorf("second = %q want grep", s.Commands[1].Program)
	}
}

func TestParseAndChainCollectsBoth(t *testing.T) {
	s := parsed(t, "git status && git diff")
	if len(s.Commands) != 2 {
		t.Fatalf("and-chain: expected 2 commands, got %d", len(s.Commands))
	}
	if s.Commands[0].Subcommand != "status" || s.Commands[1].Subcommand != "diff" {
		t.Errorf("subcommands = [%q %q] want [status diff]",
			s.Commands[0].Subcommand, s.Commands[1].Subcommand)
	}
}

func TestParseOrChainCollectsBoth(t *testing.T) {
	s := parsed(t, "make test || make build")
	if len(s.Commands) != 2 {
		t.Fatalf("or-chain: expected 2 commands, got %d", len(s.Commands))
	}
}

func TestParseSemicolonChainCollectsBoth(t *testing.T) {
	s := parsed(t, "echo hi; rm important")
	if len(s.Commands) != 2 {
		t.Fatalf("semi-chain: expected 2 commands, got %d", len(s.Commands))
	}
	if s.Commands[1].Program != "rm" {
		t.Errorf("second = %q want rm (safety-critical!)", s.Commands[1].Program)
	}
}

func TestParseTripleChainCollectsAll(t *testing.T) {
	s := parsed(t, "a && b && c")
	if len(s.Commands) != 3 {
		t.Errorf("expected 3 commands, got %d", len(s.Commands))
	}
}

func TestParseForLoopBody(t *testing.T) {
	s := parsed(t, "for f in *.go; do rm $f; done")
	found := false
	for _, c := range s.Commands {
		if c.Program == "rm" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("for-loop: expected to find rm in body; got %d commands", len(s.Commands))
	}
}

func TestParseIfBranches(t *testing.T) {
	s := parsed(t, "if [ -f foo ]; then rm foo; else mv bar baz; fi")
	programs := map[string]bool{}
	for _, c := range s.Commands {
		programs[c.Program] = true
	}
	if !programs["rm"] {
		t.Errorf("if/then: expected to find rm")
	}
	if !programs["mv"] {
		t.Errorf("if/else: expected to find mv")
	}
}

func TestParseSubshell(t *testing.T) {
	s := parsed(t, "(cd /tmp && rm -rf foo)")
	found := false
	for _, c := range s.Commands {
		if c.Program == "rm" {
			found = true
		}
	}
	if !found {
		t.Errorf("subshell: expected rm; got %d commands", len(s.Commands))
	}
}

func TestParseBraceGroup(t *testing.T) {
	s := parsed(t, "{ echo hi; rm foo; }")
	if len(s.Commands) != 2 {
		t.Errorf("brace-group: expected 2 commands, got %d", len(s.Commands))
	}
}

func TestParseCommandSubstitution(t *testing.T) {
	s := parsed(t, `echo "$(rm -rf ~/foo)"`)
	found := false
	for _, c := range s.Commands {
		if c.Program == "rm" {
			found = true
		}
	}
	if !found {
		t.Errorf("$(): expected rm inside substitution; got %d commands", len(s.Commands))
	}
}

func TestParseWhileLoopBody(t *testing.T) {
	s := parsed(t, "while read line; do echo $line; done")
	found := false
	for _, c := range s.Commands {
		if c.Program == "echo" {
			found = true
		}
	}
	if !found {
		t.Errorf("while: expected echo in body; got %d commands", len(s.Commands))
	}
}

func TestParseNestedCompound(t *testing.T) {
	// git status, ls /tmp, rm /tmp/foo — three commands across pipeline+chain
	s := parsed(t, "git status && ls /tmp | grep foo && rm /tmp/foo")
	programs := map[string]int{}
	for _, c := range s.Commands {
		programs[c.Program]++
	}
	for _, p := range []string{"git", "ls", "grep", "rm"} {
		if programs[p] == 0 {
			t.Errorf("expected to find %q in nested compound; got %v", p, programs)
		}
	}
}

func TestParseBackgroundJob(t *testing.T) {
	// Background `&` doesn't make commands invisible — `cmd &` still ran.
	s := parsed(t, "rm -rf /tmp/old &")
	if len(s.Commands) == 0 {
		t.Fatalf("background: no commands extracted")
	}
	if s.Commands[0].Program != "rm" {
		t.Errorf("background: Program = %q want rm", s.Commands[0].Program)
	}
}

func TestIsCompound(t *testing.T) {
	if parsed(t, "ls").IsCompound() {
		t.Errorf("single command should not be compound")
	}
	if !parsed(t, "a && b").IsCompound() {
		t.Errorf("a && b should be compound")
	}
}

func TestIsEnvAssignment(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"A=1", true},
		{"FOO_BAR=value", true},
		{"a=1", true},
		{"VAR2=foo", true},
		{"=value", false},
		{"2VAR=1", false},
		{"A-B=1", false},
		{"plain", false},
		{"--flag=value", false},
	}
	for _, c := range cases {
		got := isEnvAssignment(c.in)
		if got != c.want {
			t.Errorf("isEnvAssignment(%q) = %v want %v", c.in, got, c.want)
		}
	}
}
