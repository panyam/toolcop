#!/usr/bin/env bash
# End-to-end smoke test: build, start daemon, pipe a fake PreToolUse hook
# request through the client, verify the response.
set -euo pipefail

cd "$(dirname "$0")/.."

GREEN=$'\e[32m'
RED=$'\e[31m'
RESET=$'\e[0m'

pass() { echo "  ${GREEN}PASS${RESET} $1"; }
fail() { echo "  ${RED}FAIL${RESET} $1"; FAILED=1; }
FAILED=0

# Build
echo "==> Building binary"
make build > /dev/null

# Set up an isolated socket + rules file.
TMP=$(mktemp -d)
export TOOLCOP_SOCKET="/tmp/toolcop-smoke-$$.sock"
cleanup() {
    if [[ -n "${DAEMON_PID:-}" ]]; then
        kill "$DAEMON_PID" 2>/dev/null || true
        wait "$DAEMON_PID" 2>/dev/null || true
    fi
    rm -rf "$TMP"
    rm -f "$TOOLCOP_SOCKET"
}
trap cleanup EXIT

cat > "$TMP/rules.yaml" <<'EOF'
rules:
  - name: gh-readonly
    match:
      tool: Bash
      program: gh
      args_starts_with: [api]
    decide: allow
    reason: smoke gh-readonly

  - name: never-rm-rf-home
    match:
      tool: Bash
      command_regex: 'rm\s+-rf?\s+(~|\$HOME)'
    decide: deny
    reason: smoke never-rm-rf-home

  - name: gh-with-token
    match:
      tool: Bash
      program: gh
      env_has: GH_TOKEN
    decide: allow
    reason: smoke gh-with-token
EOF

echo "==> Starting daemon"
./bin/toolcop daemon --rules "$TMP/rules.yaml" > "$TMP/daemon.log" 2>&1 &
DAEMON_PID=$!

# Wait for socket to appear (up to 1s).
for _ in 1 2 3 4 5 6 7 8 9 10; do
    [[ -S "$TOOLCOP_SOCKET" ]] && break
    sleep 0.1
done
if [[ ! -S "$TOOLCOP_SOCKET" ]]; then
    fail "daemon socket never appeared"
    cat "$TMP/daemon.log"
    exit 1
fi

run_test() {
    local name="$1" want="$2" hook="$3"
    local resp
    resp=$(printf '%s' "$hook" | ./bin/toolcop)
    if printf '%s' "$resp" | grep -q "\"permissionDecision\":\"$want\""; then
        pass "$name"
    else
        fail "$name (got: $resp)"
    fi
}

echo "==> Running smoke checks"

run_test "gh api -> allow" allow \
    '{"session_id":"smoke","cwd":"/tmp","hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":"gh api repos/foo/bar"}}'

run_test "env-prefixed gh api -> allow (env stripped)" allow \
    '{"session_id":"smoke","cwd":"/tmp","hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":"GH_TOKEN=x gh api repos/foo/bar"}}'

run_test "timeout 30 gh api -> allow (wrapper stripped)" allow \
    '{"session_id":"smoke","cwd":"/tmp","hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":"timeout 30 gh api foo"}}'

run_test "rm -rf ~/foo -> deny" deny \
    '{"session_id":"smoke","cwd":"/tmp","hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":"rm -rf ~/foo"}}'

run_test "ls -> ask (no matching rule)" ask \
    '{"session_id":"smoke","cwd":"/tmp","hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":"ls -la"}}'

run_test "Read tool -> ask (no Bash rules apply)" ask \
    '{"session_id":"smoke","cwd":"/tmp","hook_event_name":"PreToolUse","tool_name":"Read","tool_input":{"file_path":"/etc/hosts"}}'

if [[ "$FAILED" -ne 0 ]]; then
    echo
    echo "${RED}smoke FAILED${RESET}"
    echo "Daemon log:"
    cat "$TMP/daemon.log"
    exit 1
fi
echo
echo "${GREEN}smoke OK${RESET}"
