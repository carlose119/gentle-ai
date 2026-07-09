package sdd

import (
	"fmt"
	"strings"

	"github.com/gentleman-programming/gentle-ai/internal/model"
)

// RenderTriggerRules renders a TriggerRuleSet as a short, scannable Markdown
// block. The output is marker-free — the caller wraps it via InjectMarkdownSection.
//
// Output format (4R v2 deterministic triage):
//   - Fixed header framing the block as a deterministic triage router the
//     orchestrator applies as a decision procedure, not advice
//   - The three-tier triage (trivial → no lens, standard → exactly ONE lens,
//     hot path / large diff → full 4R fan-out) plus a compact risk table
//   - One bullet per binding in declaration order
//
// The function is pure: no I/O, no globals mutated, no goroutines.
func RenderTriggerRules(set model.TriggerRuleSet) string {
	var sb strings.Builder

	sb.WriteString("## Agent Trigger Rules\n\n")
	sb.WriteString("Deterministic triage router. gentle-ai renders this text; the AI orchestrator ")
	sb.WriteString("applies it as a decision procedure, not advice. Triage every diff into exactly one tier before acting:\n\n")
	sb.WriteString("1. **Trivial diff** (ONLY documentation, comments, formatting, or typo fixes in strings — zero executable code and zero configuration changes): run no review lens. Any diff touching executable code or configuration is at least standard tier.\n")
	sb.WriteString("2. **Standard diff**: run exactly ONE lens — the risk-table row matching the dominant risk; do not add lenses.\n")
	sb.WriteString("3. **Hot path or large diff**: run the full 4R fan-out; never at pre-commit or pre-push.\n\n")
	// Row scopes are verbatim copies of the Review Lens Selection table in the
	// sdd-orchestrator assets — the two tables must stay in scope parity.
	sb.WriteString("Risk table (standard tier — pick ONE row): Clear naming, structure, maintainability, or small refactors → `review-readability`; ")
	sb.WriteString("Behavior, state, tests, determinism, or regressions → `review-reliability`; ")
	sb.WriteString("Shell/process integration, partial failures, recovery, or degraded dependencies → `review-resilience`; ")
	sb.WriteString("Security, permissions, data exposure/loss, architecture, or dependencies → `review-risk`.\n\n")

	for _, b := range set.Bindings {
		whenPhrase := renderWhen(b.When)
		directive := renderDirective(b)

		line := fmt.Sprintf("- At **%s**, %s: %s.", b.On, whenPhrase, directive)
		if b.Mode != model.ModeAdvisory && isFull4R(b.Run) && !b.When.Always {
			line = fmt.Sprintf("- At **%s**: %s.", b.On, directive)
		}
		if b.Reason != "" {
			line += fmt.Sprintf(" (%s)", b.Reason)
		}
		sb.WriteString(line)
		sb.WriteString("\n")
	}

	return sb.String()
}

// renderDirective converts a binding's Run list and Mode into the v2 triage
// directive for that event.
//
//   - advisory + a single review lens: the everyday router — trivial diffs run
//     no lens, everything else runs exactly ONE lens (the bound lens is the
//     default row), and the full 4R fan-out is prohibited at that event.
//   - advisory + anything else: trivial diffs are exempt, otherwise run the
//     bound agents.
//   - strong + the full 4R set under a condition: still diff triage, so the
//     trivial exemption applies; the fan-out fires when the condition matches;
//     a standard diff falls back to exactly ONE lens.
//   - strong otherwise (phase-triggered agents such as judgment-day, not diff
//     triage): run the bound agents whenever the condition matches, with no
//     trivial exemption.
func renderDirective(b model.TriggerBinding) string {
	agents := renderAgents(b.Run)

	if b.Mode == model.ModeAdvisory {
		if len(b.Run) == 1 && isReviewLens(b.Run[0]) {
			return fmt.Sprintf(
				"trivial diff → no lens; otherwise run exactly ONE lens selected by the risk table (default %s); never the full 4R fan-out here",
				agents,
			)
		}
		return fmt.Sprintf("trivial diff → no lens; otherwise run %s", agents)
	}

	// ModeStrong (and any unrecognized mode) renders as a direct directive.
	if isFull4R(b.Run) && !b.When.Always {
		condition := strings.TrimPrefix(renderWhen(b.When), "when ")
		condition = strings.ReplaceAll(condition, " OR when ", " OR ")
		condition = strings.ReplaceAll(condition, " AND when ", " AND ")
		return fmt.Sprintf("trivial diff → no lens; else if %s, run %s using the adapter's execution mode (parallel with dedicated agents; sequential inline); else run exactly ONE lens selected by the risk table", condition, renderAgentList(b.Run))
	}
	return fmt.Sprintf("run %s", agents)
}

// isReviewLens reports whether agent is one of the four 4R review lenses.
func isReviewLens(agent string) bool {
	switch agent {
	case "review-risk", "review-readability", "review-reliability", "review-resilience":
		return true
	}
	return false
}

// isFull4R reports whether run contains all four 4R review lenses.
func isFull4R(run []string) bool {
	found := map[string]bool{}
	for _, r := range run {
		if isReviewLens(r) {
			found[r] = true
		}
	}
	return len(found) == 4
}

// renderWhen converts a TriggerWhen condition into a natural-language phrase.
func renderWhen(w model.TriggerWhen) string {
	if w.Always {
		return "always"
	}

	var parts []string

	if len(w.Phases) > 0 {
		phaseList := joinPhases(w.Phases)
		return fmt.Sprintf("after the %s phase completes", phaseList)
	}

	if len(w.PathGlobs) > 0 {
		quoted := make([]string, len(w.PathGlobs))
		for i, g := range w.PathGlobs {
			quoted[i] = "`" + g + "`"
		}
		parts = append(parts, "when the diff touches "+strings.Join(quoted, ", "))
	}

	if w.MinDiffLines > 0 {
		parts = append(parts, fmt.Sprintf("when the diff exceeds %d changed lines", w.MinDiffLines))
	}

	if len(parts) == 0 {
		return "when conditions are met"
	}

	combinator := "OR"
	if w.Combine == "and" {
		combinator = "AND"
	}

	return strings.Join(parts, " "+combinator+" ")
}

// renderAgents formats the list of agent names for a binding.
func renderAgents(run []string) string {
	agents := renderAgentList(run)
	if len(run) > 1 {
		return agents + " in parallel"
	}
	return agents
}

func renderAgentList(run []string) string {
	if len(run) == 0 {
		return "(no agents)"
	}
	if len(run) == 1 {
		return fmt.Sprintf("`%s`", run[0])
	}
	quoted := make([]string, len(run))
	for i, a := range run {
		quoted[i] = "`" + a + "`"
	}
	last := quoted[len(quoted)-1]
	rest := quoted[:len(quoted)-1]
	return strings.Join(rest, ", ") + ", and " + last
}

// joinPhases joins phase names with "or" for the when-phrase.
func joinPhases(phases []string) string {
	if len(phases) == 0 {
		return ""
	}
	if len(phases) == 1 {
		return phases[0]
	}
	last := phases[len(phases)-1]
	rest := phases[:len(phases)-1]
	return strings.Join(rest, ", ") + " or " + last
}
