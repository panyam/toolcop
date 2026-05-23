package parse

import (
	"reflect"
	"testing"
)

func TestParseSimple(t *testing.T) {
	p, err := ParseBash("ls -la")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if p.Program != "ls" {
		t.Errorf("Program = %q want ls", p.Program)
	}
	if !reflect.DeepEqual(p.Args, []string{"-la"}) {
		t.Errorf("Args = %v want [-la]", p.Args)
	}
}

func TestParseEnvPrefix(t *testing.T) {
	p, err := ParseBash("A=1 B=hello gh api foo/bar")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if p.Env["A"] != "1" {
		t.Errorf("Env[A] = %q want 1", p.Env["A"])
	}
	if p.Env["B"] != "hello" {
		t.Errorf("Env[B] = %q want hello", p.Env["B"])
	}
	if p.Program != "gh" {
		t.Errorf("Program = %q want gh", p.Program)
	}
	if p.Subcommand != "api" {
		t.Errorf("Subcommand = %q want api", p.Subcommand)
	}
	if !reflect.DeepEqual(p.Args, []string{"api", "foo/bar"}) {
		t.Errorf("Args = %v want [api foo/bar]", p.Args)
	}
}

func TestParseWrapperTimeout(t *testing.T) {
	p, err := ParseBash("timeout 30 npm test")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if p.Program != "npm" {
		t.Errorf("Program = %q want npm", p.Program)
	}
	if p.Subcommand != "test" {
		t.Errorf("Subcommand = %q want test", p.Subcommand)
	}
}

func TestParseWrapperTimeoutWithFlags(t *testing.T) {
	p, err := ParseBash("timeout -k 5s 30s npm test")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if p.Program != "npm" {
		t.Errorf("Program = %q want npm", p.Program)
	}
}

func TestParseWrapperNice(t *testing.T) {
	p, err := ParseBash("nice -n 10 make build")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if p.Program != "make" {
		t.Errorf("Program = %q want make", p.Program)
	}
}

func TestParseEnvCommand(t *testing.T) {
	p, err := ParseBash("env A=1 B=2 npm test")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if p.Program != "npm" {
		t.Errorf("Program = %q want npm", p.Program)
	}
}

func TestParsePipeline(t *testing.T) {
	p, err := ParseBash("ls -la | grep foo")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !p.HasPipeline {
		t.Errorf("expected HasPipeline=true")
	}
	if p.Program != "ls" {
		t.Errorf("Program = %q want ls (first segment of pipeline)", p.Program)
	}
}

func TestParseAndChain(t *testing.T) {
	p, err := ParseBash("git status && git diff")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if p.Program != "git" {
		t.Errorf("Program = %q want git", p.Program)
	}
	if p.Subcommand != "status" {
		t.Errorf("Subcommand = %q want status", p.Subcommand)
	}
}

func TestParseQuotedArgs(t *testing.T) {
	p, err := ParseBash(`gh issue create --title "hello world"`)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if p.Program != "gh" {
		t.Errorf("Program = %q want gh", p.Program)
	}
	want := []string{"issue", "create", "--title", "hello world"}
	if !reflect.DeepEqual(p.Args, want) {
		t.Errorf("Args = %v want %v", p.Args, want)
	}
}

func TestParseEmpty(t *testing.T) {
	p, err := ParseBash("")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if p.Program != "" {
		t.Errorf("Program should be empty, got %q", p.Program)
	}
}

func TestParseSyntaxError(t *testing.T) {
	_, err := ParseBash(`echo "unterminated`)
	if err == nil {
		t.Errorf("expected error on unterminated quote")
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
		{"=value", false},      // no name
		{"2VAR=1", false},      // name starts with digit
		{"A-B=1", false},       // dash not allowed
		{"plain", false},       // no equals
		{"--flag=value", false}, // looks like a flag
	}
	for _, c := range cases {
		got := isEnvAssignment(c.in)
		if got != c.want {
			t.Errorf("isEnvAssignment(%q) = %v want %v", c.in, got, c.want)
		}
	}
}
