package api

// Verdict is the strength of a rule's or module's opinion about a tool call.
// Higher-priority verdicts override lower-priority ones during combining.
type Verdict string

const (
	// VerdictPass — no opinion; doesn't influence the final decision.
	VerdictPass Verdict = "pass"
	// VerdictAllow — would let the call through if no stronger verdict appears.
	VerdictAllow Verdict = "allow"
	// VerdictAsk — would route through Claude's prompt UI; beats Allow.
	VerdictAsk Verdict = "ask"
	// VerdictDeny — would block the call outright; beats everything.
	VerdictDeny Verdict = "deny"
)

var verdictPriority = map[Verdict]int{
	VerdictPass:  0,
	VerdictAllow: 1,
	VerdictAsk:   2,
	VerdictDeny:  3,
}

// Stronger reports whether v outranks other in the combining order.
func (v Verdict) Stronger(other Verdict) bool {
	return verdictPriority[v] > verdictPriority[other]
}

// Decision is what a single rule or module produces. The daemon combines
// every emitted Decision into one final Response.
type Decision struct {
	Verdict Verdict
	Reason  string
	Source  string // rule name or module name; used by `toolcop tail` and debug logs
	Prompt  string // optional template, used only when Verdict == VerdictAsk
}

// Helpers for module authors and rule loaders.

func Pass() Decision                  { return Decision{Verdict: VerdictPass} }
func Allow(reason string) Decision    { return Decision{Verdict: VerdictAllow, Reason: reason} }
func Deny(reason string) Decision     { return Decision{Verdict: VerdictDeny, Reason: reason} }
func Ask(reason string) Decision      { return Decision{Verdict: VerdictAsk, Reason: reason} }
func AskWith(reason, prompt string) Decision {
	return Decision{Verdict: VerdictAsk, Reason: reason, Prompt: prompt}
}

// Combine folds a slice of Decisions into one per the priority rules:
// deny > ask > allow > pass. Ties broken by first occurrence — callers
// should append rule matches in evaluation order, then module decisions
// after, so the earliest-defined rule "wins" within a priority class.
//
// When the final verdict is Pass, the caller should fall through to the
// default behavior (which for PreToolUse is `ask` — i.e., let Claude
// prompt the user normally).
func Combine(decisions []Decision) Decision {
	winner := Pass()
	for _, d := range decisions {
		if d.Verdict.Stronger(winner.Verdict) {
			winner = d
		}
	}
	return winner
}

// ToResponse maps a combined Decision to the wire Response that Claude
// expects. A Pass verdict is rendered as `ask` since the daemon must emit
// some permissionDecision; `ask` lets Claude's normal prompt UI take over.
func (d Decision) ToResponse() Response {
	decisionStr := PermissionAsk
	switch d.Verdict {
	case VerdictAllow:
		decisionStr = PermissionAllow
	case VerdictDeny:
		decisionStr = PermissionDeny
	case VerdictAsk:
		decisionStr = PermissionAsk
	}
	return Response{
		HookSpecificOutput: HookSpecificOutput{
			HookEventName:            HookEventPreToolUse,
			PermissionDecision:       decisionStr,
			PermissionDecisionReason: d.Reason,
		},
	}
}
