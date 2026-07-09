---
name: judgment-day
description: "Trigger: judgment day, dual review, adversarial review, juzgar. Run blind dual review, fix confirmed issues, then re-judge."
license: Apache-2.0
metadata:
  author: gentleman-programming
  version: "1.5"
---

## Activation Contract

Load this skill only when the user explicitly asks for Judgment Day, dual/adversarial review, or equivalent Spanish trigger (`juzgar`, `que lo juzguen`). Review a specific target: files, feature, PR, or architecture slice.

## Hard Rules

- Resolve project skills before launching agents: read skill registry, match skill paths by target files/task, and inject the same `Skills to load before work` block into both judge prompts and fix prompts.
- Launch **two blind judges in parallel** with identical target and criteria; never review the code yourself.
- Wait for both judges before synthesis; never accept a partial verdict.
- Keep every warning's canonical severity as `WARNING` and canonical status as `info`. If useful, record `assessment: real` when normal intended use can trigger it or `assessment: theoretical` otherwise; never encode that assessment in severity or set a warning to `open`.
- Ask before fixing Round 1 confirmed issues.
- After any fix agent runs, immediately re-launch both judges in parallel before commit/push/done/session summary.
- Terminal states are only `JUDGMENT: APPROVED` or `JUDGMENT: ESCALATED`.
- After 2 fix rounds, stop: report anything still open to the user as open and escalate — the loop never extends.

## Decision Gates

| Condition | Action |
|---|---|
| Target unclear | Ask for scope; do not launch judges. |
| No skill registry | Warn, proceed with generic criteria, and record `Skill Resolution: none`. |
| Both judges find same BLOCKER/CRITICAL | Confirmed; ask/fix according to round rules. |
| Judges find only warnings/suggestions | Report once as INFO; never fix, never re-judge. |
| One judge finds issue | Suspect; report and triage, do not auto-fix. |
| Judges contradict | Escalate for manual decision. |
| Judges report only warnings/suggestions | Report once with status `info`; never fix or re-judge. |

## Execution Steps

1. Confirm target and optional custom criteria.
2. Resolve exact skill paths from registry or warn if missing.
3. Start Judge A and Judge B concurrently via delegation; each runs its sweep-budgeted first pass and emits its own findings ledger.
4. Synthesize findings into confirmed, suspect, contradiction, and INFO buckets; merge both judges' ledger rows into the persisted ledger and persist per the artifact-store branch.
5. Ask before Round 1 fixes; delegate a separate fix agent for confirmed approved fixes only. The fix agent reads the persisted ledger, applies only confirmed fixes, and sets addressed ledger ids to `fixed`.
6. Re-judge in parallel after fixes, scoped to the persisted ledger and the fix diff per the Scoped re-review contract; repeat within the convergence budget (maximum 2 fix rounds), then either approve or escalate with remaining issues reported as open.
7. Before any terminal action, verify every active Judgment Day has a terminal state.

## Output Contract

Return `## Judgment Day — {target}` with round number, verdict table, confirmed/suspect/contradiction counts, fixes applied, ledger persistence location, re-judgment result, `Skill Resolution`, and final `JUDGMENT: APPROVED ✅` or `JUDGMENT: ESCALATED ⚠️`.

## Ledger and Re-Judge Contract

**Sweep budget.** Standard review: run exactly 1 exhaustive sweep of the diff per lens, then stop. Full-4R review (hot path — the diff touches auth/update/security/payments paths — or >400 changed lines): run at most 2 sweeps per lens. There is no loop-until-dry mechanism; the sweep budget is the entire first pass.

**Precision gate.** Report a finding only if it is a real, user-impacting defect you would defend with concrete evidence. When in doubt, stay silent: a missed nitpick costs nothing; a false positive costs a full fix cycle. Style and preference findings are banned unless they obscure a defect.

**Findings ledger.** Emit a findings ledger with this schema for every entry:

| Field | Values |
|-------|--------|
| `id` | `{LENS}-{NNN}` (e.g. `R1-001`) |
| `lens` | risk \| readability \| reliability \| resilience \| judgment-day |
| `location` | `path/to/file.ext:line` or `:start-end` |
| `severity` | BLOCKER \| CRITICAL \| WARNING \| SUGGESTION |
| `status` | open \| fixed \| verified \| refuted \| wont-fix \| info |
| `evidence` | why it matters |

If the first pass finds nothing, persist an empty ledger record rather than skip persistence.

**Adversarial verification.** Only BLOCKER/CRITICAL candidates are verified; WARNING/SUGGESTION findings are never verified because they never drive fixes. Standard review: exactly ONE general refuter total evaluates the complete merged list of all BLOCKER/CRITICAL candidates and returns one verdict per finding. Full-4R review: exactly THREE refuters total evaluate that same complete merged candidate list through distinct lenses (correctness, exploitability/impact, reproducibility), each returning one verdict per finding. Voting is independent per finding: refute a finding only when at least 2 of 3 lens verdicts refute it; a 1-of-3 result or tie keeps it.

**Refutation protocol.** The orchestrator invokes refutation once after merging lens ledgers and before any fix work; only BLOCKER/CRITICAL candidates are included. The task ceiling is review-level and structural: 1 refuter task for a standard review or 3 total for full-4R, whether the list has 2 candidates or 20; NEVER spawn one refuter task per candidate. Where dedicated `review-refuter` agents exist, standard review delegates exactly one task with the `general` lens, while full-4R delegates exactly three tasks, one per lens, in parallel. Every task receives the complete merged candidate list. In standard review, a finding is `refuted` only when the general verdict refutes it; in full-4R, apply the independent 2-of-3 vote per finding. Any malformed or missing per-finding verdict defaults to `stands` for that finding. Judgment Day is the exception: its two-judge convergence satisfies adversarial verification and it spawns no `review-refuter` tasks.

**Severity floor.** Only BLOCKER/CRITICAL findings that survive adversarial verification enter the fix → re-review loop. WARNING/SUGGESTION findings are reported once with status `info`, are never re-reviewed, and never block. Judgment-day may record real/theoretical as a separate `assessment`, but canonical severity remains `WARNING` and canonical status remains `info`; a WARNING is never `open`.

**Convergence budget.** Maximum 2 fix rounds per review. One fix round = the orchestrator (directly or via a single writer sub-agent) applies fixes for all open verified BLOCKER/CRITICAL findings, then a scoped re-review verifies the fix diff against the ledger; in judgment-day the fix actor is `jd-fix-agent`. Anything still open after round 2 is reported to the user as open — the loop never extends.

**Ledger persistence honors the artifact store.**
- `openspec`: write `openspec/changes/{change-name}/review-ledger.md`.
- `engram`: upsert topic `sdd/{change-name}/review-ledger` (ad-hoc judgment-day without a change: `review/{target-slug}/ledger`, where `target-slug` = `pr-{number}` when reviewing a PR, else the current branch name kebab-cased, else a kebab-case slug of the user-stated review target).
- `none`: keep the ledger inline in the response; do not write files or Engram artifacts — the ledger lives only in this conversation; complete the review → fix → re-review loop within the session because it is not persisted across compaction.

**Scoped re-review.** A re-review pass receives ONLY the persisted ledger and the fix diff as input — never the original full diff. It MUST verify each ledger finding's resolution and MUST review only fix-touched lines; it MUST NOT re-read the full original diff. A finding on an untouched line MUST be logged with status `info` as a first-pass quality signal and MUST NOT by itself trigger another full round. The re-judge pass following jd-fix-agent follows this same scoped re-review contract.

**Execution mode.** Judgment-day judges run as delegated agents; when this agent is a named sub-agent (Claude, Kiro), emit your own ledger rows and hand them to the orchestrator, which merges both judges' rows into the persisted ledger. Otherwise, the orchestrator runs both judges via generic delegate and maintains the merged ledger directly.

## References

- [references/prompts-and-formats.md](references/prompts-and-formats.md) — judge/fix prompts, warning rubric, verdict tables, and language snippets.
