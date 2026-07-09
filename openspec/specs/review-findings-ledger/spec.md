# Review findings ledger contract

## Purpose

Define the 4R v2 precision-gated review contract shared by the 4R review lenses (review-risk, review-resilience, review-readability, review-reliability) and judgment-day, across all supported adapter variants and execution modes: a fixed sweep budget, a precision gate on every finding, a persisted findings ledger, adversarial verification of high-severity candidates, a severity floor on the fix loop, a bounded convergence budget, and a re-review scoped by construction to the ledger plus the fix diff.

## Requirements

### Requirement: Fixed sweep budget for the first pass

Each 4R lens and judgment-day judge pass MUST run its first review within a fixed sweep budget instead of a loop-until-dry mechanism. A standard review MUST run exactly 1 exhaustive sweep of the diff per lens. A full-4R review (hot path — the diff touches auth/update/security/payments paths — or >400 changed lines) MUST run at most 2 sweeps per lens. The sweep budget is the entire first pass; there is no dry-sweep counting and no unbounded looping.

#### Scenario: Standard review runs exactly one sweep

- GIVEN a lens begins its first pass on a standard diff
- WHEN the sweep completes
- THEN the lens MUST stop and finalize its ledger rows
- AND it MUST NOT run additional sweeps

#### Scenario: Full-4R review is capped at two sweeps

- GIVEN a lens begins its first pass on a hot-path or >400-changed-line diff
- WHEN the second sweep completes
- THEN the lens MUST stop regardless of whether new findings surfaced
- AND no loop-until-dry mechanism is applied

---

### Requirement: Precision gate on every finding

A lens MUST report a finding only if it is a real, user-impacting defect the lens would defend with concrete evidence. When in doubt, the lens MUST stay silent: a missed nitpick costs nothing; a false positive costs a full fix cycle. Style and preference findings are banned unless they obscure a defect.

#### Scenario: Doubtful candidate is not reported

- GIVEN a lens observes a suspicious pattern it cannot back with concrete evidence
- WHEN it decides whether to report
- THEN it MUST omit the finding from the ledger

#### Scenario: Style-only finding is banned

- GIVEN a lens observes a pure style or preference issue that does not obscure a defect
- WHEN it decides whether to report
- THEN it MUST NOT report the finding

---

### Requirement: Persisted findings ledger

The first pass MUST emit a structured findings ledger. Each entry MUST identify the finding (id), originating lens, file:line location, severity (BLOCKER, CRITICAL, WARNING, or SUGGESTION), resolution status (open, fixed, verified, refuted, wont-fix, or info), and evidence.

#### Scenario: Ledger captures required fields

- GIVEN a lens completes its first pass
- WHEN it emits the ledger
- THEN each entry MUST include an id, lens, file:line location, severity, status, and evidence

#### Scenario: Zero findings still produce a ledger record

- GIVEN a first pass finds nothing
- WHEN the ledger is finalized
- THEN the system MUST persist an empty ledger record rather than skip persistence

---

### Requirement: Adversarial verification of high-severity candidates

Only BLOCKER/CRITICAL candidates MUST be adversarially verified; WARNING/SUGGESTION findings are never verified because they never drive fixes. A standard review MUST run exactly one general refuter pass total over the complete merged candidate list and receive one verdict per finding. A full-4R review MUST run exactly three refuter passes total (correctness, exploitability/impact, reproducibility); each pass receives that same complete list and returns one verdict per finding. The task ceiling is structural and review-level — 1 standard or 3 full-4R regardless of candidate count — and the system MUST NOT spawn one refuter task per candidate. Full-4R voting is independent per finding: at least 2 of 3 `refuted` verdicts refute that finding, while a 1-of-3 result or tie keeps it. Any malformed or missing per-finding verdict defaults to `stands`. A refuter MUST present concrete counter-evidence; "seems unlikely" does not refute.

#### Scenario: Standard review uses one batched refuter total

- GIVEN a standard review produces multiple BLOCKER/CRITICAL candidates
- WHEN adversarial verification runs
- THEN exactly one general refuter pass receives the complete candidate list
- AND it returns one verdict per finding
- AND each successfully refuted finding is recorded with status `refuted` and never enters the fix loop

#### Scenario: Full-4R review uses three batched lenses with a per-finding 2-of-3 rule

- GIVEN a full-4R review produces multiple CRITICAL candidates
- WHEN the three refuter lenses (correctness, exploitability/impact, reproducibility) evaluate them
- THEN exactly three refuter passes total each receive the complete candidate list
- AND each finding is killed only if at least 2 of 3 lens verdicts refute that finding
- AND a 1-of-3 result or tie keeps that finding actionable

#### Scenario: Refuter task count does not scale with findings

- GIVEN a standard or full-4R review produces 2 or 20 BLOCKER/CRITICAL candidates
- WHEN adversarial verification runs
- THEN the standard review runs exactly 1 refuter task or inline pass total
- AND the full-4R review runs exactly 3 refuter tasks or inline passes total
- AND no task or pass is created per candidate

#### Scenario: Missing or malformed verdict stands

- GIVEN a batched refuter omits one candidate or returns a malformed verdict for it
- WHEN the orchestrator merges refutation results
- THEN that finding's verdict defaults to `stands`
- AND other well-formed per-finding verdicts remain independently usable

#### Scenario: Low-severity findings are never verified

- GIVEN a first pass produces WARNING and SUGGESTION findings
- WHEN adversarial verification runs
- THEN those findings MUST NOT be sent to any refuter

#### Scenario: Refuted status is terminal

- GIVEN a finding recorded with status `refuted`
- WHEN the fix loop and re-review run
- THEN the finding MUST NOT be fixed, re-verified, or re-opened by those passes

---

### Requirement: Severity floor on the fix loop

Only BLOCKER/CRITICAL findings that survive adversarial verification MUST enter the fix → re-review loop. WARNING/SUGGESTION findings MUST be reported once with status `info`, MUST never be re-reviewed, and MUST never block.

#### Scenario: Verified high-severity finding enters the fix loop

- GIVEN a BLOCKER candidate survives adversarial verification
- WHEN the fix loop starts
- THEN the finding is actionable and routed to the fix pass

#### Scenario: Low-severity finding is informational only

- GIVEN a WARNING finding in the ledger
- WHEN the fix loop and re-review run
- THEN the finding keeps status `info`
- AND it neither triggers a fix round nor blocks completion

---

### Requirement: Convergence budget of two fix rounds

A review MUST run at most 2 fix rounds. Anything still open after round 2 MUST be reported to the user as open — the loop never extends.

#### Scenario: Open finding after round two is surfaced, not looped

- GIVEN a verified BLOCKER finding remains open after the second fix round
- WHEN the review concludes
- THEN the finding is reported to the user with status `open`
- AND no third fix round is started

---

### Requirement: Ledger persistence honors the artifact store

Ledger persistence MUST follow the session's configured artifact store: an OpenSpec change artifact when the store is `openspec`, an Engram topic when the store is `engram`, or in-context only (no file or topic write) when the store is `none`. When the store is `none`, the review → fix → re-review loop MUST complete within the session because the ledger is not persisted across compaction.

#### Scenario: Store selects persistence target

- GIVEN the artifact store is `openspec`, `engram`, or `none`
- WHEN a lens finalizes its ledger
- THEN it MUST be persisted respectively as a change artifact, an Engram topic scoped to change and ledger, or kept in-context only

#### Scenario: None store writes nothing

- GIVEN the artifact store is `none`
- WHEN a lens finalizes its ledger
- THEN no file or Engram artifact MUST be written

---

### Requirement: Re-review scoped by construction

A re-review pass MUST receive ONLY the persisted ledger and the fix diff as input — never the original full diff. It MUST verify each ledger finding's resolution and MUST review only fix-touched lines. A finding on an untouched line MUST be logged with status `info` as a first-pass quality signal and MUST NOT by itself trigger another full round.

#### Scenario: Re-review verifies ledger findings within scope

- GIVEN a persisted ledger with open findings and a fix diff addressing them
- WHEN the re-review pass runs
- THEN it MUST update each finding's status to `verified` or still-`open`
- AND it MUST NOT receive or re-read the original full diff

#### Scenario: Untouched-line finding is logged, not escalated

- GIVEN a re-review observes an issue on an untouched line
- WHEN the finding is recorded
- THEN it MUST be appended to the ledger with status `info` as a first-pass quality signal
- AND it MUST NOT by itself cause a full review round to restart

---

### Requirement: Judgment-day follows the same contract

Judgment-day's judge agents (jd-judge-a, jd-judge-b) MUST apply the same sweep budget, precision gate, and persisted-ledger contract, and the re-judge pass following jd-fix-agent MUST follow the same scoped re-review contract and convergence budget as the 4R lenses. Two-judge convergence itself satisfies adversarial verification, so judgment-day MUST NOT spawn review refuters. Every judgment-day warning keeps canonical `severity=WARNING` and `status=info`; real/theoretical MAY be recorded as a separate assessment but MUST NOT alter severity or status. WARNING/SUGGESTION rows never enter fix or re-review loops.

#### Scenario: Judgment-day first pass is budgeted and ledgered

- GIVEN jd-judge-a or jd-judge-b runs a first judgment pass
- WHEN the pass completes
- THEN it MUST stay within the sweep budget, apply the precision gate, and persist a findings ledger per the artifact-store contract

#### Scenario: Re-judge is scoped and bounded

- GIVEN jd-fix-agent has applied fixes for ledgered findings
- WHEN the re-judge pass runs
- THEN it MUST verify ledger findings and review only fix-touched lines
- AND it MUST respect the two-fix-round convergence budget

#### Scenario: Judgment-day warning stays informational

- GIVEN either judge reports a warning assessed as real or theoretical
- WHEN the merged ledger is written
- THEN its canonical severity is `WARNING`
- AND its canonical status is `info`, never `open`
- AND it does not enter the fix or re-judge loop

---

### Requirement: Contract coverage across adapter variants and execution modes

The sweep-budget, precision-gate, ledger, adversarial-verification, severity-floor, convergence-budget, and scoped re-review contract MUST be present across all 13 supported adapter variants and both execution modes. Dedicated-agent adapters (Claude, Kiro, Cursor, Kimi, OpenCode/Kilocode) merge review-agent ledger rows and run exactly 1 batched refuter task for standard review or 3 for full-4R; only the 3 full-4R tasks may run in parallel. Inline adapters run review lenses sequentially and MUST override generic delegation wording for refutation: they run one general pass or three sequential lens passes inside the orchestrator, each over the complete merged candidate list, without spawning refuter tasks.

#### Scenario: Both execution modes carry the contract

- GIVEN an adapter with dedicated subagents, or one without
- WHEN its review-*/jd-* assets, or its orchestrator's inline-lens section, are inspected
- THEN they MUST contain the sweep-budget, precision-gate, ledger, adversarial-verification, severity-floor, convergence-budget, and scoped re-review contract worded for that execution mode
- AND inline adapters MUST state that their sequential in-orchestrator refutation clause overrides generic delegation wording

#### Scenario: No adapter variant is left uncovered

- GIVEN all 13 adapter variants are enumerated
- WHEN contract coverage is evaluated
- THEN every variant MUST include the contract in its review and judgment-day-related assets
