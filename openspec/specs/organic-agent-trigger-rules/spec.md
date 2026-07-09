# Agent Trigger Rules Specification

The domain slug `organic-agent-trigger-rules` is historical (the v1 rules were framed as "organic recommendations"); the directory and Domain field keep it for continuity.

**Domain**: `organic-agent-trigger-rules`  
**Type**: NEW Capability  
**Change**: organic-agent-trigger-rules  
**Status**: FINAL (Implemented and Verified)

## Purpose

Define a declarative trigger-rules system that gentle-ai INSTALLS (not executes) into every supported agent. The system defines a closed set of lifecycle events, a binding schema with structured `when` conditions, a token-aware built-in default rule set, and mechanism for rendering rules as plain instructional text and injecting them idempotently into all supported agent assets through the existing installer/injection path.

The rendered text is a deterministic triage router (4R v2), not an advisory suggestion list. The orchestrator triages every diff into exactly one tier before acting: (1) trivial diff (ONLY documentation, comments, formatting, or typo fixes in strings — zero executable code and zero configuration changes; any diff touching executable code or configuration is at least standard tier) → no review lens; (2) standard diff → exactly ONE lens selected by the risk table; (3) hot path (auth/update/security/payments paths) or >400 changed lines → the full 4R fan-out, on pre-pr only. gentle-ai itself still never fires, executes, or blocks anything: the router is instruction text the orchestrator applies as a decision procedure.

---

## Requirements

### Requirement: Closed Event Set

The system MUST define exactly the following six events and no others. Any `on` value outside this set MUST be treated as invalid.

| Event | Trigger Moment | Meaning |
|-------|----------------|---------|
| `pre-commit` | Before a commit is created | Diff exists; final code is staged but not yet committed. |
| `pre-push` | Before a local branch is pushed to a remote | All commits for the push are local; the remote has not received them. |
| `pre-pr` | Before a Pull Request is opened or marked ready-for-review | The branch is pushed; a PR is about to be created or promoted from draft. |
| `post-sdd-phase` | Immediately after an SDD phase completes | Phase name is known (e.g., `design`, `apply`). Used for phase-scoped agents. |
| `on-ci` | When a CI pipeline run is triggered | Includes push-triggered, PR-triggered, and manual CI runs. |
| `on-schedule` | On a recurring schedule | Interval is defined in configuration; intended for periodic review sweeps. |

#### Scenario: Catalog contains exactly six events

- GIVEN the events catalog in `internal/catalog/`
- WHEN a test iterates all registered events
- THEN exactly six events are returned: `pre-commit`, `pre-push`, `pre-pr`, `post-sdd-phase`, `on-ci`, `on-schedule`
- AND no additional events are present

#### Scenario: Unknown event name is rejected

- GIVEN a binding with `on: "post-merge"`
- WHEN the binding is validated against the catalog
- THEN validation returns an error indicating `"post-merge"` is not a recognized event
- AND the binding is not added to the active rule set

#### Scenario: Valid event name is accepted

- GIVEN a binding with `on: "pre-pr"`
- WHEN the binding is validated against the catalog
- THEN validation passes
- AND the binding is included in the active rule set

### Requirement: Event Definitions Are Exported and Testable

Each event MUST be defined as a named constant (or equivalent) in the Go model layer so tests can reference event names without using raw strings.

---

### Requirement: Binding Fields — Required and Optional

Each binding MUST have the following fields:

| Field | Required | Valid Values | Description |
|-------|----------|--------------|-------------|
| `on` | YES | Any value in the supported events catalog | The lifecycle event that triggers this binding. |
| `run` | YES | One or more agent identifiers from the supported agent set | The agent(s) to route to. Must be non-empty. |
| `when` | YES | Any value in the `when` vocabulary | The condition that must be satisfied for the binding to be active. |
| `mode` | NO (default: `advisory`) | `advisory` or `strong` | Behavioral mode governing directive language in the rendered output. |
| `reason` | NO | Free-form string | Internal documentation field that records WHY the binding exists (its token-budget justification). |

The four fields `on`, `run`, `when`, and `mode` are the core binding contract. `reason` is the ONLY permitted optional addition. No other arbitrary fields are allowed.

#### Scenario: Binding with all fields is valid

- GIVEN a binding `{ on: "pre-pr", when: { MinDiffLines: 400 }, run: ["review-risk"], mode: "strong" }`
- WHEN the binding is validated
- THEN validation passes
- AND the binding is included in the rule set

#### Scenario: Binding missing `on` is invalid

- GIVEN a binding `{ when: { Always: true }, run: ["review-readability"], mode: "advisory" }` with no `on` field
- WHEN the binding is validated
- THEN validation returns an error indicating `on` is required
- AND the binding is rejected

#### Scenario: Binding missing `run` is invalid

- GIVEN a binding `{ on: "pre-commit", when: { Always: true }, mode: "advisory" }` with no `run` field
- WHEN the binding is validated
- THEN validation returns an error indicating `run` must be non-empty
- AND the binding is rejected

#### Scenario: Binding missing `when` is invalid

- GIVEN a binding `{ on: "pre-commit", run: ["review-readability"], mode: "advisory" }` with no `when` field
- WHEN the binding is validated
- THEN validation returns an error indicating `when` is required
- AND the binding is rejected

#### Scenario: Binding with unknown `mode` is invalid

- GIVEN a binding with `mode: "blocking"`
- WHEN the binding is validated
- THEN validation returns an error indicating `"blocking"` is not a valid mode value
- AND the binding is rejected

#### Scenario: Binding without `mode` defaults to `advisory` rendering

- GIVEN a binding `{ on: "pre-commit", when: { Always: true }, run: ["review-readability"] }` with no `mode` field
- WHEN the binding is processed
- THEN `mode` is set to `advisory`

#### Scenario: Binding with `reason` field is valid

- GIVEN a binding with a `reason` field containing token-budget justification
- WHEN the binding is validated
- THEN validation passes
- AND the `reason` value is preserved on the binding struct for inspection
- AND `reason` is NOT emitted in any user-facing output

#### Scenario: Binding with an unknown extra field is invalid

- GIVEN a binding `{ on: "pre-commit", when: { Always: true }, run: ["review-readability"], mode: "advisory", color: "blue" }`
- WHEN the binding is validated
- THEN validation returns an error indicating `"color"` is an unrecognized field
- AND the binding is rejected

### Requirement: `run` Field — Agent Identifier Set

The set of valid agent identifiers for the `run` field MUST cover at minimum: `review-risk`, `review-readability`, `review-reliability`, `review-resilience`, `judgment-day`, plus all eight SDD phase identifiers (`sdd-explore`, `sdd-propose`, `sdd-spec`, `sdd-design`, `sdd-tasks`, `sdd-apply`, `sdd-verify`, `sdd-archive`).

An `run` entry that is not a recognized agent identifier MUST be treated as invalid.

#### Scenario: Unknown agent identifier in `run` is rejected

- GIVEN a binding with `run: ["review-seo"]`
- WHEN the binding is validated
- THEN validation returns an error indicating `"review-seo"` is not a recognized agent identifier
- AND the binding is rejected

#### Scenario: Multiple agents in `run` are all valid

- GIVEN a binding with `run: ["review-risk", "review-resilience"]`
- WHEN the binding is validated
- THEN validation passes for both identifiers
- AND the binding is included in the rule set

### Requirement: Rule Set as an Ordered List

The full trigger-rules configuration MUST be represented as an ordered list of bindings. Bindings are evaluated in list order; all matching bindings for an event fire.

#### Scenario: Multiple bindings for the same event

- GIVEN a rule set with two bindings both having `on: "pre-pr"`
- WHEN the rule set is evaluated for event `pre-pr`
- THEN both bindings are included in the evaluation output
- AND neither silently suppresses the other

---

### Requirement: Closed `when` Vocabulary

The system MUST support exactly the following `when` condition forms and no others in this change. The vocabulary is structured, closed, and rendered to plain instructional text.

| Form | Meaning |
|------|---------|
| `Always: true` | The binding activates unconditionally at the named event. |
| `MinDiffLines: N` | The binding activates when the cumulative changed-line count exceeds N (a positive integer). |
| `PathGlobs: [...]` | The binding activates when at least one changed file matches any of the listed glob patterns. |
| `PathGlobs + MinDiffLines (Combine: "or")` | The binding activates when EITHER the diff touches any of the named globs OR the line count exceeds N. |
| `Phases: [...]` | Valid only on `post-sdd-phase` events. The binding activates when the completed SDD phase name is one of the listed values. |

#### Scenario: `always` condition is valid and accepted

- GIVEN a binding with `when: { Always: true }`
- WHEN the binding is validated
- THEN validation passes

#### Scenario: `MinDiffLines` with a positive integer is valid

- GIVEN a binding with `when: { MinDiffLines: 400 }`
- WHEN the binding is validated
- THEN validation passes

#### Scenario: `MinDiffLines` with a non-positive integer is invalid

- GIVEN a binding with `when: { MinDiffLines: 0 }` or `when: { MinDiffLines: -10 }`
- WHEN the binding is validated
- THEN validation returns an error indicating N must be a positive integer

#### Scenario: `PathGlobs` with at least one glob is valid

- GIVEN a binding with `when: { PathGlobs: ["**/auth/**"] }`
- WHEN the binding is validated
- THEN validation passes

#### Scenario: `PathGlobs` with no globs is invalid

- GIVEN a binding with `when: { PathGlobs: [] }`
- WHEN the binding is validated
- THEN validation returns an error indicating at least one glob is required

#### Scenario: Compound `PathGlobs` OR `MinDiffLines` is valid

- GIVEN a binding with `when: { PathGlobs: ["**/auth/**"], MinDiffLines: 400, Combine: "or" }`
- WHEN the binding is validated
- THEN validation passes

#### Scenario: Unsupported boolean combinator is invalid

- GIVEN a binding with an unsupported combinator like `and` or `not`
- WHEN the binding is validated
- THEN validation returns an error indicating the combinator is not supported

#### Scenario: `Phases` is valid on `post-sdd-phase` event

- GIVEN a binding with `on: "post-sdd-phase"` and `when: { Phases: ["design", "apply"] }`
- WHEN the binding is validated
- THEN validation passes

#### Scenario: `Phases` is invalid on non-`post-sdd-phase` events

- GIVEN a binding with `on: "pre-commit"` and `when: { Phases: ["design"] }`
- WHEN the binding is validated
- THEN validation returns an error indicating `Phases` is only valid for `post-sdd-phase`

#### Scenario: Invalid `when` value is rejected

- GIVEN a binding with an unrecognized condition form
- WHEN the binding is validated
- THEN validation returns an error indicating the condition form is not recognized

### Requirement: `when` Conditions Render to Self-Explanatory Text

Each supported `when` form MUST have a deterministic, human-readable rendering that makes the condition unambiguous.

#### Scenario: `Always` renders to unconditional directive

- GIVEN a binding with `when: { Always: true }`
- WHEN the renderer produces the instructional text
- THEN the rendered output contains language such as "unconditionally" or "at every occurrence of this event"

#### Scenario: `MinDiffLines` renders to line-count instruction

- GIVEN a binding with `when: { MinDiffLines: 400 }`
- WHEN the renderer produces the instructional text
- THEN the rendered output states a threshold of 400 changed lines in plain language

#### Scenario: `PathGlobs` renders with glob values visible

- GIVEN a binding with `when: { PathGlobs: ["**/auth/**", "**/update/**"] }`
- WHEN the renderer produces the instructional text
- THEN the rendered output names the path patterns explicitly

#### Scenario: `Phases` renders with phase names visible

- GIVEN a binding with `when: { Phases: ["design", "apply"] }`
- WHEN the renderer produces the instructional text
- THEN the rendered output names the phases explicitly

---

### Requirement: Two Valid Modes

The system MUST support exactly two mode values: `advisory` and `strong`. Any other value MUST be treated as invalid.

### Requirement: `advisory` Mode Semantics

A binding with `mode: advisory` MUST render as the everyday triage routing: a trivial diff runs no lens; any other diff runs exactly ONE lens selected by the risk table, with the bound agent stated as the default row. For a single-review-lens binding the rendering MUST also prohibit the full 4R fan-out at that event.

Representative rendered language for `advisory`:
- "trivial diff → no lens; otherwise run exactly ONE lens selected by the risk table (default `review-readability`); never the full 4R fan-out here"

The rendered text MUST NOT use advisory suggestion phrasing ("consider running", "you may want to", "it is recommended") — the routing is a decision procedure, not advice.

#### Scenario: `advisory` binding renders the trivial-diff exemption routing

- GIVEN a binding `{ on: "pre-commit", when: { Always: true }, run: ["review-readability"], mode: "advisory" }`
- WHEN the renderer produces the instructional text
- THEN the rendered output routes a trivial diff to no lens and any other diff to exactly ONE lens selected by the risk table
- AND the rendered output does not contain suggestion phrases like "consider running" or "strongly recommend"

#### Scenario: Omitted `mode` defaults to `advisory` rendering

- GIVEN a binding with no `mode` field (default applied)
- WHEN the renderer produces the instructional text
- THEN the rendered output is identical to an explicit `mode: "advisory"` binding with the same other fields

### Requirement: `strong` Mode Semantics

A binding with `mode: strong` MUST render as a direct run directive the orchestrator applies whenever the binding's condition matches. The trivial-diff exemption follows the binding's nature, not its mode: a `strong` binding that triages a diff (the full 4R fan-out under a non-`Always` condition, e.g. pre-pr) MUST render the trivial-diff exemption AND the standard-diff fallback (when the condition does not match, run exactly ONE lens selected by the risk table); a `strong` binding for a phase-triggered, non-diff-triage agent (e.g. `judgment-day` at `post-sdd-phase`) renders without the trivial-diff exemption. The binding MUST NOT create a hard gate or block forward progress.

Representative rendered language for `strong`:
- "run `judgment-day`"
- "trivial diff → no lens; else if the hot-path or large-diff condition matches, run `review-risk`, `review-resilience`, `review-readability`, and `review-reliability` using the adapter's execution mode (parallel with dedicated agents; sequential inline); else run exactly ONE lens selected by the risk table"

The rendered text MUST NOT contain language implying the workflow is blocked or paused pending the review.

#### Scenario: `strong` binding renders a direct run directive

- GIVEN a binding `{ on: "pre-pr", when: { MinDiffLines: 400 }, run: ["review-risk", "review-resilience"], mode: "strong" }`
- WHEN the renderer produces the instructional text
- THEN the rendered output directs the orchestrator to run the bound agents when the condition matches
- AND the rendered output does not imply blocking or mandatory confirmation

#### Scenario: `strong` conditional full-4R binding carries the trivial-diff exemption

- GIVEN a binding `{ on: "pre-pr", when: { PathGlobs: ["**/auth/**"], MinDiffLines: 400, Combine: "or" }, run: [all four 4R lenses], mode: "strong" }`
- WHEN the renderer produces the instructional text
- THEN the rendered output routes a trivial diff to no lens before the fan-out directive
- AND it uses one exhaustive trivial / hot-or-large / standard decision with no consecutive `otherwise` branches
- AND the rendered output states the standard-diff fallback (exactly ONE lens selected by the risk table)

#### Scenario: `strong` phase-triggered binding renders without the trivial-diff exemption

- GIVEN a binding `{ on: "post-sdd-phase", when: { Phases: ["design", "apply"] }, run: ["judgment-day"], mode: "strong" }`
- WHEN the renderer produces the instructional text
- THEN the rendered output is a direct run directive with no trivial-diff exemption (the agent is phase-triggered, not diff triage)

#### Scenario: `strong` is the highest enforcement level — no blocking

- GIVEN any binding with `mode: "strong"`
- WHEN the rendered instructional text is inspected
- THEN the text contains no language that implies the workflow is paused, blocked, or requires explicit user confirmation
- AND the text contains no language equivalent to "you must not proceed until", "gate", "block", or "halt"

### Requirement: Mode Differences Are Testable

A test MUST assert that `advisory` and `strong` renderings of identical bindings produce observably different output.

#### Scenario: `advisory` and `strong` renderings differ

- GIVEN two bindings identical except for `mode: "advisory"` vs `mode: "strong"`
- WHEN both are rendered
- THEN the rendered outputs are not equal
- AND the `advisory` output carries the trivial-diff exemption routing that the `strong` output omits

---

### Requirement: Default Rule Set Exists and Is Non-Empty

A built-in default rule set MUST exist in `internal/catalog/` and MUST contain bindings for the following events:
- `pre-commit` (everyday routing: trivial → no lens, otherwise exactly ONE lens; advisory, single default lens)
- `pre-push` (everyday routing: trivial → no lens, otherwise exactly ONE lens; advisory, single default lens)
- `pre-pr` (strong, full 4R fan-out on hot paths or large diffs; standard diffs fall back to exactly ONE lens)
- `post-sdd-phase` (strong, judgment-day on design/apply phases)

`on-ci` and `on-schedule` MUST each have zero default bindings (with rationale documented in code).

#### Scenario: Default rule set is non-empty

- GIVEN the built-in default rule set in `internal/catalog/`
- WHEN a test loads the defaults
- THEN the returned list is non-empty
- AND at least one binding exists for `pre-commit`, `pre-push`, `pre-pr`, and `post-sdd-phase`

### Requirement: Everyday Events — Trivial Diffs Skip Review, Standard Diffs Get Exactly ONE Lens

| Binding | `on` | `when` | `run` | `mode` |
|---------|------|--------|-------|--------|
| Default pre-commit routing | `pre-commit` | `{ Always: true }` | `["review-readability"]` | `advisory` |
| Default pre-push routing | `pre-push` | `{ Always: true }` | `["review-readability"]` | `advisory` |

The bound lens is the default row of the risk table for that event; the rendered routing sends trivial diffs to no lens and every other diff to exactly ONE lens. The full 4R fan-out is prohibited at these events.

#### Scenario: Default pre-commit binding is advisory and single-agent

- GIVEN the built-in default rule set
- WHEN it is searched for bindings with `on: "pre-commit"`
- THEN at least one binding is returned
- AND that binding has `mode: "advisory"`
- AND that binding's `run` list contains exactly one agent identifier
- AND the agent is `"review-readability"`

#### Scenario: Default pre-push binding does not trigger the 4R fan-out

- GIVEN the built-in default rule set
- WHEN it is searched for bindings with `on: "pre-push"`
- THEN no binding with `on: "pre-push"` runs all four 4R agents simultaneously

### Requirement: Hot Paths or Large Diffs — Full 4R Fan-Out (pre-pr only)

| Binding | `on` | `when` | `run` | `mode` |
|---------|------|--------|-------|--------|
| Pre-PR hot-path 4R | `pre-pr` | `{ PathGlobs: ["**/auth/**", "**/update/**"], MinDiffLines: 400, Combine: "or" }` | `["review-risk", "review-readability", "review-reliability", "review-resilience"]` | `strong` |

The hot-path glob set MUST include at minimum: `**/auth/**`, `**/update/**`. The diff-line threshold MUST be `400` and MUST be implemented as a named constant. The rendered binding MUST state one exhaustive decision: a trivial pre-pr diff runs no lens; otherwise a hot-path or large diff runs full 4R; otherwise the standard diff runs exactly ONE lens selected by the risk table. Full 4R runs in parallel only where dedicated agents exist and sequentially inside inline adapters.

#### Scenario: Default pre-pr binding triggers the full 4R under the compound condition

- GIVEN the built-in default rule set
- WHEN it is searched for bindings with `on: "pre-pr"`
- THEN at least one binding is returned
- AND that binding's `run` list contains all four 4R agents
- AND that binding's `when` has a compound condition with `PathGlobs` and `MinDiffLines` combined by `Combine: "or"`
- AND that binding has `mode: "strong"`

#### Scenario: The 400-line threshold is a named constant

- GIVEN the catalog package source
- WHEN a reviewer inspects the Go source defining the default pre-pr binding
- THEN the number `400` does not appear as a raw integer literal
- AND a named constant (e.g., `defaultLargeChangedLineThreshold`) is referenced instead

#### Scenario: The pre-pr 4R binding does NOT fire on small, off-hot-path diffs

- GIVEN the default pre-pr binding's `when` condition
- AND a hypothetical diff of 50 lines touching only `internal/tui/colors.go`
- WHEN the orchestrator evaluates the condition
- THEN the condition evaluates to false
- AND the 4R fan-out is NOT triggered

### Requirement: Judgment-Day at High-Stakes Moments Only

| Binding | `on` | `when` | `run` | `mode` |
|---------|------|--------|-------|--------|
| Post-SDD design/apply judge | `post-sdd-phase` | `{ Phases: ["design", "apply"] }` | `["judgment-day"]` | `strong` |

`judgment-day` MUST NOT appear in any default binding for `pre-commit` or `pre-push`.

#### Scenario: Default post-sdd-phase binding targets only design and apply phases

- GIVEN the built-in default rule set
- WHEN it is searched for bindings with `on: "post-sdd-phase"`
- THEN at least one binding is returned
- AND the binding's `when.Phases` includes at minimum `"design"` and `"apply"`
- AND no other phases are included in the default

#### Scenario: `judgment-day` does not appear in pre-commit or pre-push defaults

- GIVEN the built-in default rule set
- WHEN all bindings with `on: "pre-commit"` or `on: "pre-push"` are enumerated
- THEN none of them include `"judgment-day"` in their `run` list

### Requirement: Default Rule Set Is Validated at Load Time

The built-in defaults MUST pass all schema validations. A test MUST assert this so schema changes that invalidate the defaults are caught immediately.

#### Scenario: All default bindings pass schema validation

- GIVEN the built-in default rule set
- WHEN each binding is run through the validator
- THEN all bindings pass without errors

---

### Requirement: Token-Budget Requirement

`when` is a first-class cost controller. The default rule set MUST be demonstrably tuned so that a normal development day stays within a small, predictable token cost.

#### Scenario: Expensive agent without restrictive `when` is not permitted in defaults

- GIVEN the default rule set
- WHEN all bindings are inspected
- THEN no binding exists with expensive multi-agent `run` lists paired with `when: { Always: true }`
- (Such a binding would violate token budget.)

#### Scenario: Expensive agent with restrictive `when` is accepted

- GIVEN a binding `{ on: "pre-pr", when: { PathGlobs: [...], MinDiffLines: 400, Combine: "or" }, run: [4R agents], mode: "strong" }`
- WHEN the binding is evaluated
- THEN it passes token-budget validation because the condition limits fan-out to high-blast-radius changes

#### Scenario: Normal-day token profile is bounded

- GIVEN the default rule set
- AND a profile of: 5 pre-commit events, 2 pre-push events, each with diffs under 100 lines touching no hot-path globs
- AND 1 pre-pr event with a diff of 200 lines touching no hot-path globs
- WHEN each binding's `when` condition is evaluated analytically
- THEN each event routes to at most exactly ONE lens (trivial diffs route to none)
- AND the full 4R fan-out and `judgment-day` are never triggered

#### Scenario: Hot-path PR triggers the full 4R fan-out

- GIVEN the default rule set
- AND a pre-pr event with a diff touching `**/auth/**`
- WHEN each binding's `when` condition is evaluated
- THEN the hot-path pre-pr binding activates
- AND all four 4R agents run in parallel where dedicated agents exist or sequentially inside an inline adapter

#### Scenario: Token-Budget Rationale Is Documented in Code

- GIVEN the Go source file defining the built-in default rule set
- WHEN a reviewer reads the file
- THEN a comment exists that references the tiered triage/budget model (trivial → none, standard → one lens, hot path → full 4R, SDD → judgment-day)
- AND the comment explains why everyday single-lens bindings use `{ Always: true }` and why the full-4R binding requires a compound condition

---

### Requirement: Rendering

The renderer turns the rule set into per-agent plain instructional text. The rendered block MUST be:
- Self-contained: a reader with no knowledge of the schema understands what to do
- Scannable: events, conditions, agents, and modes are clearly labeled
- Concise: the entire trigger-rules section MUST NOT exceed 40 lines for the default rule set
- Deterministic: the same rule set produces byte-identical output on every render
- Marker-free: the renderer does NOT emit `<!-- gentle-ai:trigger-rules -->` markers (the caller injects them)

#### Scenario: RenderTriggerRules is deterministic

- GIVEN the default rule set
- WHEN `RenderTriggerRules` is called twice
- THEN both outputs are byte-identical

#### Scenario: Rendered output is marker-free

- GIVEN rendered output from `RenderTriggerRules`
- WHEN the output is inspected
- THEN it contains no `<!-- gentle-ai:` or `<!-- /gentle-ai:` markers
- (Markers are added by the injector, not the renderer.)

#### Scenario: Rendered block frames itself as a deterministic triage router

- GIVEN rendered output from `RenderTriggerRules`
- WHEN the output is inspected
- THEN it contains language indicating the block is a deterministic triage router the orchestrator applies as a decision procedure, not advice
- AND it states the three triage tiers explicitly, including the trivial-diff → no-lens tier
- AND it contains no v1 advisory phrasing ("organic recommendations", "consider running", "strongly recommend")

#### Scenario: Rendered output does not exceed line budget

- GIVEN the default rule set
- WHEN rendered to instructional text
- THEN the output is no longer than 40 lines

---

### Requirement: Injection

Rules are injected through the existing installer path. The injector writes the rendered block into the installed assets for every supported agent through the existing `filemerge.InjectMarkdownSection` mechanism under the section ID `gentle-ai:trigger-rules`.

### Requirement: All Eight Supported Agents Must Receive Injected Rules

The injection MUST target ALL of the following supported agents:

1. `claude`
2. `opencode`
3. `cursor`
4. `codex`
5. `gemini`
6. `vscode`
7. `windsurf`
8. `antigravity`

No supported agent may be silently skipped. A test MUST enumerate all eight adapters and assert that each one's installed asset contains the trigger-rules section.

#### Scenario: All eight agents receive the trigger-rules section

- GIVEN the trigger-rules injection has run
- WHEN each of the eight agent assets is inspected
- THEN each asset contains the trigger-rules marker section
- AND the section contains at least one rendered binding

#### Scenario: A newly added agent adapter triggers a test failure until injection is wired

- GIVEN a ninth agent adapter is added to the factory
- WHEN the injection coverage test runs
- THEN it fails because the ninth adapter is not covered

### Requirement: Injection Uses the Existing Marker-Section Mechanism

The trigger-rules section MUST use a dedicated, uniquely named marker: `gentle-ai:trigger-rules`.

The marker-section mechanism is the same one used for existing sections (via `internal/filemerge/section.go`). No new injection mechanism is introduced.

#### Scenario: Marker section is present after injection

- GIVEN an agent asset file before injection
- WHEN injection runs
- THEN the file contains an opening marker `<!-- gentle-ai:trigger-rules -->` and a corresponding closing marker
- AND the rendered directive block is between the markers

#### Scenario: Injection is idempotent

- GIVEN an agent asset that already contains the `gentle-ai:trigger-rules` marker section
- WHEN injection runs again with the same rule set
- THEN the file content is identical
- AND the marker section does not appear more than once

#### Scenario: Injection updates stale content

- GIVEN an agent asset that contains a `gentle-ai:trigger-rules` section with outdated rendered content
- WHEN injection runs with a newer rule set
- THEN the old section content is replaced with the new rendered content
- AND the markers remain present and unique

### Requirement: Injected Content Is Plain Instructional Text

The rendered block injected into each agent asset MUST be plain, human-readable instructional text. It MUST NOT be YAML, TOML, JSON, or any structured data format that requires a parser to interpret.

#### Scenario: Rendered default rule set is plain text and self-contained

- GIVEN the default rule set rendered for any adapter
- WHEN a human reads the rendered block
- THEN every binding is described in a complete sentence or short paragraph in English
- AND no YAML, TOML, or JSON syntax is present
- AND the reader understands what event, condition, agents, and routing mode apply without consulting any other document

### Requirement: Per-Agent Phrasing May Vary; Semantics Must Not

The renderer MAY produce agent-specific phrasing where needed. However, the semantic content — which agents run at which events under which conditions with which mode — MUST be identical across all adapters for the same binding.

#### Scenario: Same binding produces semantically equivalent output across adapters

- GIVEN the default pre-pr strong binding
- WHEN it is rendered for the `claude` adapter and for the `codex` adapter
- THEN both outputs describe the same event, condition, agents, and mode
- AND the specific wording may differ but the intent is identical

### Requirement: Injection Primary Placement

The primary injection point for the trigger-rules section MUST be the per-agent system-prompt or orchestrator section — the location guaranteed to be loaded at every session.

#### Scenario: Trigger-rules section is present in the always-loaded system prompt section

- GIVEN a `claude` installation produced by `gentle-ai install`
- WHEN the installed CLAUDE.md (or equivalent always-loaded file) is inspected
- THEN the `gentle-ai:trigger-rules` section is present
- AND it is not only in a secondary or optional file

---

### Requirement: No Execution of Agents

gentle-ai MUST NOT execute, spawn, or invoke any agent at any lifecycle moment. The binary's role is strictly to install and inject.

#### Scenario: Binary source contains no agent-dispatch code

- GIVEN all Go source files introduced or modified by this change
- WHEN a reviewer inspects for process-launch patterns
- THEN no such calls exist that are attributable to the trigger-rules feature
- AND `exec.Command`, `os/exec`, and process-launch patterns are absent from the new code paths

#### Scenario: Integration test confirms no side effects at install time

- GIVEN a test that runs the installer with the trigger-rules feature active
- WHEN the installer completes
- THEN no agents were invoked
- AND only file-system writes (the injected sections) occurred

### Requirement: No Git Hook Generation

gentle-ai MUST NOT create, write, or modify any file under a `.git/hooks/` directory as a result of this change.

#### Scenario: No `.git/hooks/` writes occur during install

- GIVEN a test that runs the installer in a temporary git repository
- WHEN the installer completes
- THEN no files under `.git/hooks/` were created or modified

### Requirement: No Event Bus, Daemon, Listener, or Scheduler

The change MUST NOT introduce any runtime component that listens for events, dispatches messages, maintains persistent state across invocations, or runs on a schedule.

#### Scenario: No long-running goroutines introduced

- GIVEN the Go source for the trigger-rules code
- WHEN a reviewer inspects for goroutine launches or blocking loops
- THEN none are found that are attributable to the trigger-rules feature

### Requirement: No Deterministic Gate or Hard Block

No code path introduced by this change MUST block, pause, or gate the user's workflow.

#### Scenario: Installer completes without interactive gate

- GIVEN the trigger-rules injection running as part of `gentle-ai install`
- WHEN the installer writes the trigger-rules section
- THEN the installer does not pause for input or wait for acknowledgment

#### Scenario: Rendered `strong` directive contains no blocking language

- GIVEN any binding rendered with `mode: "strong"`
- WHEN the rendered text is inspected
- THEN it contains no language equivalent to "you must not proceed", "blocked until", "halted", or "awaiting confirmation"

### Requirement: No `when` Evaluation Engine

gentle-ai MUST NOT evaluate `when` conditions at runtime. It renders them as text. The binary MUST NOT contain a parser or evaluator that reads diff metadata and applies condition logic.

#### Scenario: No diff-reading logic in trigger-rules code

- GIVEN all Go source files introduced by this change
- WHEN a reviewer inspects for calls that read git diff output or count changed lines
- THEN no such calls exist in code paths reachable from the trigger-rules code

### Requirement: No New Parse Dependency

This change MUST NOT introduce a YAML, TOML, or other structured-data parse library as a new `go.mod` dependency.

#### Scenario: `go.mod` is unchanged with respect to parse dependencies

- GIVEN the `go.mod` file before and after this change
- WHEN a diff is produced
- THEN no new parse-library entries (YAML, TOML, INI, etc.) appear
- AND the set of direct dependencies in `go.mod` either stays the same or changes only for non-parse-library additions

---

## Non-Goals

| Non-goal | Reason |
|----------|--------|
| Executing agents | gentle-ai stays an installer/injector. It renders rules; the AI tool's orchestrator runs them. |
| Generating git hooks | Events are semantic moments honored by the orchestrator, not OS-level hooks. |
| Event bus / runtime dispatch | No runtime layer is added; no daemon, no listener, no scheduler. |
| Hard gates / workflow blocking | The router is deterministic instruction text, not an enforcement mechanism. gentle-ai never blocks; the orchestrator routes reviews without pausing the user's workflow. |
| A `when` expression engine that gentle-ai evaluates | gentle-ai does not evaluate `when`; it renders the condition as instruction text for the orchestrator to interpret. |
| User-override / per-project rule customization | Explicitly deferred. Defaults ship only in this change. |

---

## Type Model Definition

The schema is implemented as Go structs with `encoding/json` tags (for future override loading). The following types are defined in `internal/model/types.go`:

```go
type TriggerEvent string

const (
    EventPreCommit    TriggerEvent = "pre-commit"
    EventPrePush      TriggerEvent = "pre-push"
    EventPrePR        TriggerEvent = "pre-pr"
    EventPostSDDPhase TriggerEvent = "post-sdd-phase"
    EventOnCI         TriggerEvent = "on-ci"
    EventOnSchedule   TriggerEvent = "on-schedule"
)

type TriggerMode string

const (
    ModeAdvisory TriggerMode = "advisory"
    ModeStrong   TriggerMode = "strong"
)

type TriggerWhen struct {
    Always       bool     `json:"always,omitempty"`
    PathGlobs    []string `json:"path_globs,omitempty"`
    MinDiffLines int      `json:"min_diff_lines,omitempty"`
    Phases       []string `json:"phases,omitempty"`
    Combine      string   `json:"combine,omitempty"` // "or" (default) | "and"
}

type TriggerBinding struct {
    On     TriggerEvent `json:"on"`
    When   TriggerWhen  `json:"when"`
    Run    []string     `json:"run"`
    Mode   TriggerMode  `json:"mode"`
    Reason string       `json:"reason,omitempty"` // ONLY optional field
}

type TriggerRuleSet struct {
    Events   []TriggerEvent   `json:"events"`
    Bindings []TriggerBinding `json:"bindings"`
}
```

---

## Implementation Locations

| Component | Location |
|-----------|----------|
| Type model | `internal/model/types.go` |
| Supported events catalog | `internal/catalog/triggers.go` (new) |
| Binding schema validator | `internal/catalog/triggers.go` |
| Default rule set | `internal/catalog/triggers.go` |
| Renderer | `internal/components/sdd/triggerrules.go` (new) |
| Injection integration | `internal/components/sdd/inject.go` (modified, step 1c) |
| Kimi template update | `internal/assets/kimi/KIMI.md` (modified, add include) |
| Tests | `internal/model/types_test.go`, `internal/catalog/triggers_test.go`, `internal/components/sdd/triggerrules_test.go`, `internal/components/sdd/inject_test.go` |
| Golden files | `internal/testdata/golden/trigger-rules-default.golden` (new) |

---

## Success Criteria (From Proposal)

- [x] A declarative trigger-rules schema (events, bindings with `when` + `mode`, execution policy) is defined and documented.
- [x] A closed supported-events catalog exists (pre-commit, pre-push, pre-pr, post-sdd-phase, on-ci, on-schedule).
- [x] A token-aware built-in default rule set ships covering the 4R, judgment-day, and sdd phases.
- [x] Default bindings demonstrably scale by blast radius: trivial diffs skip review and standard diffs get exactly ONE lens on everyday events; full 4R only on hot paths / large diffs; judgment-day only at high-stakes moments.
- [x] Rules are rendered and injected into the installed assets for ALL supported agents (claude, opencode, cursor, codex, gemini, vscode, windsurf, antigravity), idempotently.
- [x] gentle-ai adds NO execution, NO git hooks, NO event bus, NO deterministic gate — verified by the absence of any runtime dispatch code.
- [x] `go build ./...`, `go vet ./...`, and `go test ./...` pass clean.
