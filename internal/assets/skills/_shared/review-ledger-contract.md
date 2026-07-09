# Review Ledger Contract (shared across the 4R review lenses and judgment-day)

Canonical source of truth for the 4R v2 precision-gated review: the sweep
budget, the precision gate, the persisted findings ledger, adversarial
verification of high-severity candidates, the severity floor and convergence
budget for the fix loop, the artifact-store persistence branches, and the
scoped re-review/re-judge contract. Every review-* subagent asset, every jd-*
subagent asset, every orchestrator's inline-lens "Review Execution Contract"
section, and the judgment-day skill docs hand-copy the clauses below verbatim
so a single table-driven test (`internal/components/sdd/review_ledger_contract_test.go`)
can assert they stay in sync across all 13 adapter variants and both
execution modes.

Why this exists: the v1 contract optimized for maximum recall — an exhaustive
loop-until-dry first pass in which every severity entered the fix loop. In
practice that traded precision for recall: low-confidence and style-level
findings triggered full fix cycles, and repeat sweeps re-sampled the same
noise. 4R v2 replaces that with a fixed sweep budget, a precision gate on
every finding, adversarial verification before a finding becomes actionable,
a severity floor on the fix loop, and a hard convergence budget. The
persisted ledger and the re-review scoped to the ledger plus the fix diff are
retained from v1.

## Canonical block (hand-copy verbatim into every adopting asset)

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

**Scoped re-review.** A re-review pass receives ONLY the persisted ledger and the fix diff as input — never the original full diff. It MUST verify each ledger finding's resolution and MUST review only fix-touched lines; it MUST NOT re-read the full original diff. A finding on an untouched line MUST be logged with status `info` as a first-pass quality signal and MUST NOT by itself trigger another full round.

## Notes on the schema (not part of the hand-copied block)

**Sweep budget rationale.** One exhaustive sweep with a precision gate finds
the defects worth fixing; repeat sweeps mostly re-sample noise. Full-4R
reviews get a second sweep because hot paths and large diffs justify the
extra recall. The budget also caps review cost deterministically — there is
no dry-sweep counting.

**Precision over recall.** A false positive costs a refuter run plus a
potential fix cycle plus a re-review; a missed nitpick costs nothing. Every
lens therefore reports only defects it would defend with concrete evidence.

**Status lifecycle.** `open` (first-pass candidate) → adversarial
verification: a BLOCKER/CRITICAL candidate that survives refutation stays
`open` and becomes actionable; a refuted candidate becomes `refuted` and is
terminal — it never enters the fix loop. Actionable findings then move `open`
→ `fixed` (fix agent changed code) → `verified` (re-review confirmed
resolved). `wont-fix` = accepted/deferred with reason. `info` = a
WARNING/SUGGESTION finding (reported once, never verified, never re-reviewed,
never blocking) or a new finding on an untouched line (first-pass quality
signal, NOT a re-round trigger). Judgment-day may preserve real/theoretical as
a separate `assessment`, but every warning keeps canonical `severity=WARNING`
and `status=info`; warnings are never `open`.

**Refuter lenses.** In the full-4R panel, `correctness` asks "is the claimed
defect actually wrong behavior?", `exploitability/impact` asks "can a real
user or attacker ever hit it, and does it matter?", and `reproducibility`
asks "can the failure scenario be concretely reproduced from the cited
code?". In standard single-refuter mode the lens is `general` — the refuter
attacks the finding from any angle. A refuter must present concrete
counter-evidence; "seems unlikely" does not refute.

**Refuter role and delegation.** The refuter role is the `review-refuter`
agent asset (`internal/assets/{claude,cursor,kimi,kiro}/agents/review-refuter.md`
plus the OpenCode/Kilocode overlay definition). It is structurally read-only,
receives the complete merged candidate list (`id`, `location`, `severity`,
`summary`, `evidence` per entry) plus one refutation lens, and returns one
`refuted` or `stands` verdict with evidence per finding. Inconclusive,
malformed, or missing per-finding results default to `stands`. Delegation
shape:

```
Standard review (exactly one batched refuter total, general lens):
  delegate(agent="review-refuter", prompt="Candidates: [{id, location, severity, summary, evidence}, ...]. Refutation lens: general. Return one `refuted` or `stands` verdict plus evidence for every candidate.")

Full-4R review (exactly three batched refuters total, one per lens, in parallel where dedicated agents exist):
  delegate(agent="review-refuter", prompt="Candidates: [{…}, ...]. Refutation lens: correctness. Return one verdict per candidate. …")
  delegate(agent="review-refuter", prompt="Candidates: [{…}, ...]. Refutation lens: exploitability-impact. Return one verdict per candidate. …")
  delegate(agent="review-refuter", prompt="Candidates: [{…}, ...]. Refutation lens: reproducibility. Return one verdict per candidate. …")
```

Never create one task per candidate. Adapters without dedicated refuter
subagents do not use generic delegation for this step; they run the equivalent
one general pass or three sequential lens passes inline over the complete list.

**Judgment-day reconciliation.** In judgment-day, adversarial verification is
satisfied by the two-judge convergence itself: a BLOCKER/CRITICAL confirmed by
both blind judges has survived adversarial verification; judgment-day does NOT
additionally spawn `review-refuter` agents.

**Judgment-day.** The re-judge pass (following jd-fix-agent) follows this
same scoped re-review contract: it verifies ledger findings and reviews only
fix-touched lines, within the same convergence budget.

## Execution modes

The contract above is stated once; only ledger ownership differs by mode:

- **Dedicated-agent mode** (Claude, Cursor, Kimi, Kiro, OpenCode/Kilocode):
  each review-* agent runs its lens within the sweep budget and returns its own
  ledger rows; the orchestrator merges those rows, then uses exactly 1 batched
  refuter task for standard review or exactly 3 for full-4R.
- **Inline mode** (Codex, Gemini, Qwen, Windsurf, Antigravity, Hermes,
  generic, and any adapter without dedicated review/refuter subagents): the
  orchestrator runs review lenses sequentially and performs the equivalent
  one general or three sequential lens refutation passes itself.

## Interfaces / Contracts

Canonical ledger row, rendered identically in every asset:

```
| id     | lens        | location            | severity | status  | evidence            |
|--------|-------------|---------------------|----------|---------|---------------------|
| R1-001 | risk        | internal/x.go:42    | CRITICAL | open    | secret hardcoded    |
| R1-002 | risk        | internal/z.go:10    | BLOCKER  | refuted | refuter: input validated upstream |
| JD-004 | judgment-day| internal/y.go:88    | WARNING  | info    | theoretical path    |
```

## Adopting assets

Hand-copy the sections above (Sweep budget, Precision gate, Findings ledger
schema, Adversarial verification, Refutation protocol, Severity floor,
Convergence budget, Ledger persistence, Scoped re-review) into:

- `internal/assets/{claude,cursor,kimi,kiro}/agents/review-{risk,readability,reliability,resilience}.md`
- `internal/assets/{claude,kiro}/agents/jd-{judge-a,judge-b}.md`
- Every `internal/assets/*/sdd-orchestrator.md` (Review Execution Contract section)
- `internal/assets/skills/judgment-day/SKILL.md` and `references/prompts-and-formats.md`

Exception: `internal/assets/{claude,kiro}/agents/jd-fix-agent.md` is NOT a
hand-copy target for this judge-oriented block. It carries the distinct
fix-agent clause set enforced by `requiredFixAgentClauses` in the test below —
the fix role applies confirmed fixes and does not run the first-pass review
sweep or emit a findings ledger. `references/prompts-and-formats.md` carries
both: judge clauses in the Judge Prompt template, fix clauses in the Fix
Agent Prompt template.

Exception: `internal/assets/{claude,cursor,kimi,kiro}/agents/review-refuter.md`
is likewise NOT a hand-copy target. The refuter verifies a complete candidate
list and never reviews a diff or emits a findings ledger, so it carries its
own role clause set enforced by `requiredRefuterClauses` in the test below.

Each surface also states its own execution-mode sentence per the "Execution
modes" section above. `internal/components/sdd/review_ledger_contract_test.go`
enforces this parity with a table-driven `requiredLedgerClauses` consistency
check.
