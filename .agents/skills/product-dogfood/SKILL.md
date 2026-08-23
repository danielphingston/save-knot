---
name: product-dogfood
description: >
  Dogfood a running application as a real user, identify missing product
  capabilities, incomplete CRUD/lifecycle actions, missing bulk operations,
  recovery gaps, inconsistencies, and UX friction. Use agent-browser to operate
  the product before inspecting its implementation. When Codex Luna subagents
  are available, use them for bounded independent audit lanes and source
  classification. Then consolidate findings as UI gaps, backend gaps, product
  gaps, bugs, or other product-quality issues.
---

# Product Dogfood

## Objective

Use the application like a real user and determine what is missing, awkward,
incomplete, inconsistent, or unnecessarily repetitive.

This is not primarily a visual-design audit.

Focus on questions such as:

- "Why can I do this for one item but not all of them?"
- "Why can I add this but not remove it?"
- "Why can I change this but not reset it?"
- "Why can I see a failure but not retry it?"
- "Why do I have to repeat the same action many times?"
- "What happens when this operation fails halfway through?"
- "Can I undo or recover from this?"
- "Can I tell what state the product is actually in?"
- "What would a normal user reasonably expect to exist here?"

The goal is to uncover missing product features and incomplete workflows,
not just broken buttons or cosmetic problems.

---


# Mandatory Requirement: agent-browser

This skill requires the `agent-browser` CLI for browser automation.

Do not substitute another browser automation system for the product-exploration
passes. The purpose of this requirement is to ensure the agent can actually use
the running application as a user rather than infer behavior from source code.

Before beginning any audit, verify that `agent-browser` is installed, its
browser runtime is usable, and it can actually open and interact with the target
application.

Merely finding `agent-browser` in a package manifest, skill directory, shell
history, or documentation is not sufficient. The agent must prove that the CLI
works in the current environment.

## Required Preflight

Before reading application source code or starting the product audit:

1. Determine the application's URL or start the application using only the
   minimum setup information necessary.
2. Verify that `agent-browser` is executable, for example with
   `agent-browser --help`.
3. Use `agent-browser` to open the actual application URL.
4. Use `agent-browser snapshot -i` (or the installed equivalent) to confirm the
   rendered UI can be inspected.
5. Perform at least one harmless user-level interaction or navigation through
   `agent-browser` when appropriate.
6. Re-snapshot or otherwise inspect the resulting UI state through
   `agent-browser`.
7. Only after this succeeds may the audit continue.

The preflight must prove real browser control. A successful process exit alone
is not enough.

## If agent-browser Is Missing or Unusable

STOP the audit immediately.

Do not:

- inspect the application source code
- infer the product from routes, components, APIs, tests, or schemas
- perform a static-only UX/product review
- substitute `curl`, raw HTTP requests, screenshots, DOM dumps, Playwright MCP,
  Chrome DevTools MCP, Selenium, Puppeteer, or another browser automation system
- produce a partial product-dogfood report
- claim that the application was dogfooded

Tell the user clearly which prerequisite failed.

If the `agent-browser` command is missing, ask the user to install it. A suitable
response is:

> Product dogfooding requires `agent-browser`, and it is not available in the
> current environment. Please install it and its browser runtime, then rerun the
> audit. A typical setup is `npm install -g agent-browser` followed by
> `agent-browser install`.

If `agent-browser` exists but its browser runtime is missing, ask the user to
run:

```bash
agent-browser install
```

If `agent-browser` exists but cannot launch, connect to the application, take an
interactive snapshot, or operate the page, stop and report that specific
blocker.

Do not install `agent-browser` or its browser runtime automatically unless the
user explicitly asks you to do so.

Do not fall back to another audit method.

---

# Luna Subagents

Use Codex Luna subagents when they are available and suitable for the current
environment.

Luna is an execution/review helper, not the audit controller.

The primary agent owns:

- `agent-browser` preflight
- audit scope
- task decomposition
- final prioritization
- conflict resolution between findings
- final report
- any product decisions
- any decision to modify the application

Do not hand the entire audit to one subagent.

## Luna Availability Preflight

After the mandatory `agent-browser` preflight succeeds:

1. Determine whether native Codex subagents are available.
2. Determine whether a Luna-capable subagent can be selected.
3. Determine whether that subagent can execute `agent-browser` from its shell.
4. Do not guess model/tool availability from configuration files alone.
5. Only assign browser dogfooding work to a Luna subagent if that subagent can
   actually execute `agent-browser` and control the running application.

Luna is optional.

If Luna is unavailable, continue the audit in the primary agent.

Do not stop the audit merely because Luna is unavailable.

`agent-browser` remains mandatory.

## Preferred Parallel Audit Lanes

When Luna subagents can use `agent-browser`, prefer 2-4 independent bounded lanes
rather than one large delegated task.

Good lanes include:

### Lane A — Lifecycle and Symmetry

Explore entities and look for incomplete lifecycle operations:

- create/add without remove/delete
- edit without reset
- disable without re-enable
- override without restore-default
- history without cleanup
- connection without disconnect/reconnect

### Lane B — Repetition and Bulk Operations

Use the application with enough data to expose repeated workflows.

Look for:

- one-item actions that should plausibly support selected/all
- repeated manual operations
- missing batch actions
- missing "all failed", "all stale", or "all eligible" operations
- workflows that become unreasonable at 10, 20, or 100 items

Do not tell the subagent which specific bulk feature you expect it to find.

### Lane C — Failure and Recovery

Exercise realistic failure states and look for:

- failure with no retry
- failure with no explanation
- partial completion ambiguity
- missing reconnect/reconfigure action
- interrupted long-running work
- stale state after restart
- destructive action without undo/recovery

### Lane D — Navigation and Capability Discoverability

Explore whether important capabilities are available where users naturally need
them.

Look for:

- actions only hidden in deep detail screens
- inconsistent actions between list and detail views
- missing contextual actions
- state that is visible but not actionable
- dead ends
- capabilities that exist but are hard to discover

Do not use Luna merely to generate generic UX commentary.

## Isolation Between Browser Subagents

Independent observations are valuable only if the agents do not interfere with
each other.

When possible:

- give each browser subagent a separate `agent-browser` session when supported
- give each subagent an independent seeded scenario or resettable fixture
- avoid concurrent destructive actions against the same test data
- avoid concurrent writes to the same local application state
- assign read-only/exploratory lanes when environment isolation is uncertain

If independent `agent-browser` sessions or independent application state
cannot be guaranteed, run browser lanes sequentially or keep browser operation
in the primary agent.

Do not trade audit correctness for parallelism.

## Required Luna Task Packet

Every Luna subagent assignment must be narrow and explicit.

Include:

- the running application URL
- the scenario/fixture name, if any
- the exact audit lane
- whether `agent-browser` is executable and usable by the subagent
- a strict prohibition on source inspection during product exploration
- a strict prohibition on modifying application code
- safety constraints
- expected evidence format
- instruction to report uncertainty rather than infer behavior

Do not prime the subagent with expected missing features.

Bad:

> Check whether Sync All is missing.

Good:

> Explore repeated operations across lists and multi-item workflows. Identify
> operations that become unnecessarily repetitive as item count grows.

## Browser-Capable Luna Rules

A Luna subagent performing a product-exploration lane must:

1. Use `agent-browser` through its own shell access.
2. Operate the actual running application.
3. Not inspect source code, routes, APIs, tests, schemas, or implementation docs.
4. Not modify source code.
5. Record only behavior it directly observed.
6. Distinguish observed behavior from inferred user expectation.
7. Return concise evidence to the primary agent.
8. Stop its lane if `agent-browser` becomes unavailable or loses browser control.

If `agent-browser` is not executable or usable by that Luna subagent, do not
assign it a browser lane.

## Non-Browser Luna Use

If Luna subagents are available but cannot use `agent-browser`, they may still be
used after the primary agent completes passes 1 through 4.

Useful post-observation Luna tasks include:

- inspect source for an already-observed finding
- determine whether an internal capability already exists
- classify a finding as UI GAP / BACKEND GAP / PRODUCT GAP / BUG
- locate relevant routes/services/actions/tests
- estimate implementation scope
- independently challenge whether a proposed feature is justified
- identify duplicate findings across evidence packets

A non-browser Luna subagent must never claim that it personally observed the UI.

Give it the primary agent's observation packet and ask it to reason only from
that evidence plus the source code it is allowed to inspect.

## Independent Judgment

Do not ask multiple Luna subagents to simply confirm the primary agent's
conclusion.

Use them to create independent perspectives.

When two agents disagree:

1. Preserve both claims temporarily.
2. Return to `agent-browser` evidence where possible.
3. Reproduce the behavior in the primary agent.
4. Prefer directly observed behavior over speculation.
5. Record unresolved ambiguity explicitly.

Do not manufacture consensus.

## Subagent Evidence Format

Each Luna finding should return:

```text
ID:
Lane:
Observed:
Steps:
Expected user capability:
Why it matters:
Evidence:
Confidence:
Questions/uncertainty:
```

For browser lanes, `Evidence` should include enough information to reproduce the
observation, such as:

- page/screen
- control labels
- relevant visible state
- steps performed
- resulting state
- screenshot reference if available

Do not require verbose prose.

## Suggested Concurrency

Default to at most 4 active audit subagents.

Prefer fewer, well-separated lanes over many tiny agents.

A typical full audit:

```text
Primary
├── Luna A: lifecycle/symmetry
├── Luna B: repetition/bulk
├── Luna C: failure/recovery
└── Luna D: discoverability/consistency
```

Then the primary agent:

```text
collect
→ deduplicate
→ reproduce high-impact findings
→ inspect implementation
→ classify
→ prioritize
→ report
```

The primary agent should personally reproduce high-confidence P0/P1 findings
before presenting them as confirmed whenever reproduction is practical.

## Luna During Fix Mode

When the user explicitly asks to fix accepted findings, Luna may be used for
bounded implementation work.

Good Luna implementation tasks are:

- one clearly defined UI action
- one missing route wiring
- one small CRUD operation
- one focused test addition
- one straightforward state-handling fix

Do not delegate ambiguous product design decisions to Luna.

For every implementation task:

- define exact scope
- define files/components if known
- define acceptance criteria
- prohibit unrelated refactors
- require tests/verification
- have the primary agent review the result
- rerun the affected journey through `agent-browser`

The primary agent remains responsible for integration and final acceptance.

---

# Core Rule: Product First, Code Second

Do not inspect the source code, routes, API handlers, database schema, tests,
or internal documentation before completing the first product pass.

The first pass must be based only on what a user can observe and interact with.

Why:

Reading the implementation first gives you privileged knowledge that a user
does not have. It makes unclear workflows appear obvious and hides missing
affordances.

Only inspect the implementation after you have recorded the product-level
findings.

Exceptions:

- You may use commands required to start the application.
- You may read setup instructions necessary to launch the app.
- You may inspect logs only when the UI itself exposes insufficient information
  to continue, and you must record that as a product finding.
- If the requested test environment cannot be reached without implementation
  knowledge, inspect the minimum necessary setup information and continue.

---

# Safety

Treat the application as a development/test environment unless explicitly told
otherwise.

Before performing destructive or externally visible actions:

1. Determine whether the environment is safe to modify.
2. Prefer test data.
3. Do not delete production data, spend money, send messages, publish content,
   or modify real user accounts without explicit permission.
4. If a destructive workflow needs testing but cannot safely be executed,
   inspect the available UI up to the confirmation boundary and record what
   remains unverified.

Do not mark untested behavior as confirmed.

---

# Audit Method

Run the audit only after the mandatory `agent-browser` preflight succeeds.

After the `agent-browser` preflight, check whether Luna subagents are available.
Use them for bounded independent lanes when doing so preserves test isolation
and evidence quality.

The audit has five passes.

1. Cold-use exploration with `agent-browser`
2. Capability inventory
3. Completeness and symmetry analysis
4. Failure/recovery analysis with `agent-browser`
5. Implementation inspection and classification

Do not skip directly to implementation.

All user-visible observations in passes 1 through 4 must come from actually
operating the application through `agent-browser`, whether by the primary agent
or a browser-capable Luna subagent. Source inspection begins only in pass 5.

The primary agent must consolidate and deduplicate all subagent findings before
classification and prioritization.

---

# Pass 1: Cold-Use Exploration

## Act like a new user

Open the running application and attempt to understand it without source-code
knowledge.

Explore every major screen and primary workflow.

Try to answer:

- What is this product for?
- What is the primary action?
- What can I create?
- What can I modify?
- What can I remove?
- What can I sync/import/export/restore/retry?
- What happens when there are many items?
- What happens when there are no items?
- What happens after an error?
- What does "done" or "synced" actually mean?
- How do I recover from a mistake?

Do not merely click every button mechanically.

Form realistic goals and try to accomplish them.

Examples:

- Set up the product from a fresh state.
- Add or discover an item.
- Modify it.
- Perform the primary operation.
- Perform the same operation on several items.
- Reverse a previous action.
- Recover from a failure.
- Return after restarting the app.
- Find historical state.
- Clean up something no longer wanted.

## Record friction immediately

When something causes hesitation, record it before continuing.

Examples:

- The next action is unclear.
- A capability seems like it should exist but does not.
- An operation requires repetitive work.
- An object can be created but not deleted.
- There is no visible recovery path.
- Two screens expose inconsistent actions.
- A status is technically present but not understandable.
- A user must know an implementation-specific term.
- A user must open logs or source code to understand what happened.

Do not rationalize missing behavior based on what the implementation probably
does.

---

# Pass 2: Capability Inventory

Identify the product's important nouns/entities.

Examples:

- project
- account
- game
- save path
- snapshot
- device
- job
- provider
- connection
- rule
- override
- file
- backup

Build a capability matrix for each relevant entity.

Use columns when applicable:

| Entity | List | View | Create/Add | Edit | Delete/Remove | Enable/Disable | Reset | Retry | Bulk | Search/Filter |
|---|---|---|---|---|---|---|---|---|---|---|

Do not assume every entity needs every operation.

The matrix is a prompt for reasoning, not a checklist that must be filled.

For every missing cell, ask:

> Would a normal user reasonably need this operation?

Only create a finding when there is a plausible user need.

---

# Pass 3: Completeness and Symmetry Analysis

Use the following probes aggressively.

## A. Create → Manage → Remove

Whenever the app allows something to be added or created, ask:

- Can I view it afterward?
- Can I edit it?
- Can I remove/delete it?
- Can I disable it without deleting it?
- Can I restore it after removing it, if appropriate?
- Can I reset it to the detected/default value?
- Can I tell whether it is user-created or system-created?

Examples:

- Add custom path → can it be removed?
- Override image → can it be reset?
- Add account → can it be disconnected?
- Create rule → can it be disabled?
- Create snapshot → can it be deleted?

## B. One → Many

Whenever an operation exists for one item, ask whether it should exist for:

- selected items
- all items
- all eligible items
- all failed items
- all stale/outdated items

Examples:

- Sync → Sync selected / Sync all
- Retry → Retry all failed
- Delete → Delete selected
- Enable → Enable all
- Refresh → Refresh all

Do not recommend bulk actions when the operation is dangerous or rarely repeated
unless the product provides appropriate selection/confirmation.

## C. Change → Undo / Reset

Whenever the user can customize or override something, ask:

- Can I undo it?
- Can I reset to automatic/default?
- Is the original value still visible?
- Can I distinguish inherited/detected/custom state?

## D. Failure → Recovery

Whenever failure is possible, ask:

- Is the failure visible?
- Is the reason understandable?
- Can the user retry?
- Can the user change the relevant configuration?
- Does retry resume or restart?
- Can a failed operation be dismissed?
- Does the product leave ambiguous partial state?

## E. Long-running operation → Control

For sync, upload, scan, import, export, indexing, migration, restore, etc., ask:

- Is progress visible?
- Can it be cancelled?
- Can it be retried?
- Is partial completion safe?
- What happens if the app closes?
- What happens if the network disappears?
- What happens if the same action is triggered twice?

## F. List → Manage

Whenever a screen contains a list, ask:

- Search?
- Sort?
- Filter?
- Multi-select?
- Bulk actions?
- Empty state?
- Error state?
- Loading state?
- Pagination/virtualization if the list can grow?
- Does each row expose the most common action?

Only recommend these where list size and user goals justify them.

## G. State → Explanation

Whenever the app exposes states such as:

- synced
- pending
- dirty
- stale
- failed
- disabled
- disconnected
- partially complete

ask:

- Does the user understand what the state means?
- Is the state local or remote?
- When did it last change?
- What action moves it forward?
- Is "success" actually durable/completed, or only queued?

## H. Detail Action → Contextual Action

If an action exists only after drilling into a detail screen, ask whether the
most common version should also exist in the parent list/dashboard.

Likewise, if an action exists in a list, verify that the detail screen provides
an equivalent or better capability.

## I. Repetition → Automation

Notice repetitive sequences.

Examples:

- clicking Sync on 20 rows
- removing old items individually
- manually refreshing several sources
- re-entering the same configuration
- repeatedly dismissing the same type of failure

Ask whether the product should provide:

- bulk action
- sensible default
- automatic behavior
- remembered preference
- rule/policy
- one-click shortcut

## J. Lifecycle Completion

For every important entity, think through its full lifecycle:

```text
discover/create
→ configure
→ use
→ update
→ fail
→ recover
→ disable
→ delete
→ restore/recreate
```

Look for missing stages.

---

# Pass 4: User-Journey Stress Tests

After normal exploration, deliberately test edge cases.

Use those relevant to the product:

- Fresh install / no data
- One item
- Many items
- Invalid configuration
- Missing resource
- Offline/network failure
- Permission failure
- Duplicate action
- Interrupted action
- Application restart
- Stale remote/local state
- Partially completed job
- Deleted/moved source data
- Conflicting data
- Reconnect after disconnect
- Large history/list
- Wrong automatic detection
- User override
- Reset override

For every edge case ask:

1. Can the user understand what happened?
2. Can the user recover without developer knowledge?
3. Does the UI provide the next useful action?
4. Does the product preserve data safely?
5. Is there a dead end?

---

# Pass 5: Inspect the Implementation

Only now inspect the source code.

For each recorded product-level finding, determine whether the underlying
capability already exists.

Classify findings as one of:

## UI GAP

The backend/domain capability exists but the product does not expose it.

Example:

```text
Observed:
Custom save paths can be added but not removed.

Implementation:
DELETE /paths/:id already exists.

Classification:
UI GAP
```

## BACKEND GAP

The user-facing need is clear, but an underlying operation is missing.

Example:

```text
Observed:
Failed sync jobs cannot be retried.

Implementation:
No retry/resume operation exists.

Classification:
BACKEND GAP
```

## PRODUCT GAP

A useful capability is missing across the product and requires a product
decision or new behavior spanning multiple layers.

Example:

```text
Observed:
Every game can be synced individually but there is no Sync All.

Implementation:
No bulk orchestration exists.

Classification:
PRODUCT GAP
```

## BUG

The product already intends to support the behavior, but it does not work.

## UX FRICTION

The capability exists and functions, but the workflow is unnecessarily hard,
unclear, repetitive, or badly placed.

## CONSISTENCY GAP

Equivalent objects or screens behave differently without a useful reason.

## RECOVERY GAP

A failure/mistake can occur, but the user lacks a reasonable recovery path.

Do not downgrade a product gap into "UX friction" merely because a workaround
exists.

---

# Source Inspection Questions

Once code inspection is allowed, examine:

- routes/endpoints
- commands/actions
- domain services
- event handlers
- database models
- existing but unused functions
- feature flags
- hidden/context-menu actions
- tests describing intended capabilities
- TODO/FIXME notes relevant to observed gaps

Look specifically for operations that exist internally but are not reachable
from the UI.

This often reveals high-value low-effort improvements.

Example:

```text
API supports:
- syncOne
- syncMany
- deleteSnapshot
- resetOverride

UI exposes:
- syncOne

Findings:
- Sync selected/all missing
- Delete snapshot missing
- Reset override missing
```

---

# Feature-Gap Reasoning Rules

A missing action is not automatically a missing feature.

Before recommending something, ask:

1. What user goal does this solve?
2. How often is that goal likely to occur?
3. Is there already a simpler path?
4. Would adding it increase dangerous complexity?
5. Is it consistent with the product's purpose?
6. Can the same problem be solved automatically instead?
7. Is this a real observed need or speculative feature creep?

Prefer:

- high-frequency friction
- missing lifecycle operations
- obvious bulk operations
- recovery paths
- undo/reset
- visibility of important state
- capabilities already supported internally

Avoid speculative "wouldn't it be cool if..." features unless they solve a
clear observed problem.

---

# Priority Model

Score findings using:

## Impact

- `P0` — data loss, security issue, destructive behavior, or core workflow impossible
- `P1` — major missing capability or severe workflow dead end
- `P2` — meaningful friction or repetitive work
- `P3` — useful polish / lower-frequency improvement

## Confidence

- `High` — directly observed and clearly needed
- `Medium` — strongly implied by workflow
- `Low` — plausible but speculative

## Effort

After inspecting implementation:

- `XS` — trivial UI/action wiring
- `S` — localized change
- `M` — multiple components/services
- `L` — architectural/new subsystem

Prefer recommendations with:

```text
high impact
+ high confidence
+ low/moderate effort
```

---

# Required Finding Format

Every finding must use this structure:

## [ID] Short finding title

**Type:** UI GAP | BACKEND GAP | PRODUCT GAP | BUG | UX FRICTION | CONSISTENCY GAP | RECOVERY GAP  
**Priority:** P0 | P1 | P2 | P3  
**Confidence:** High | Medium | Low  
**Effort:** XS | S | M | L

**Observed**

What happened while using the actual application.

**User expectation**

What a reasonable user would expect and why.

**Why it matters**

The practical consequence: repetition, dead end, ambiguity, risk, etc.

**Suggested behavior**

Describe the smallest product change that fixes the problem.

**Implementation status**

After source inspection, state whether support already exists and where.

**Acceptance criteria**

Concrete observable behavior that would make this finding resolved.

Example:

## P-004 Add "Sync All"

**Type:** PRODUCT GAP  
**Priority:** P1  
**Confidence:** High  
**Effort:** S

**Observed**

The dashboard contains 14 games. Each row has an individual Sync action.
Synchronizing all games requires repeating the same action 14 times.

**User expectation**

The same safe independent operation applies to every game, so a user reasonably
expects an aggregate action.

**Why it matters**

Initial setup and manual reconciliation require unnecessary repeated work.

**Suggested behavior**

Add `Sync All` to the dashboard. If only some games require syncing, show the
eligible count before execution.

**Implementation status**

`syncGame()` exists. No bulk orchestration currently exists.

**Acceptance criteria**

- Dashboard exposes `Sync All`.
- Disabled games are skipped.
- Already-current games are skipped or clearly handled.
- Progress shows aggregate and per-game status.
- Individual failures do not abort unrelated games.
- Failed games remain retryable.

---

# Report Structure

At the end of the audit, produce:

# Product Dogfood Report

## Executive Summary

3-8 sentences covering the largest product-level weaknesses.

## Highest-Value Findings

A table:

| ID | Finding | Type | Priority | Confidence | Effort |
|---|---|---|---|---|---|

Order by practical value, not discovery order.

## Capability Matrix

Include the entity capability matrix from Pass 2.

## Findings

Full findings using the required format.

## Missing Bulk Operations

Explicitly list all plausible missing:

- selected-item actions
- all-item actions
- failed-item actions
- stale-item actions

Do not include unjustified ones.

## Lifecycle Gaps

List entities where add/edit/delete/reset/recovery is incomplete.

## Recovery Gaps

List errors or interruptions that leave the user without a clear next step.

## Internal Capabilities Not Exposed

After code inspection, list backend/domain operations that already exist but are
not reachable through the product.

These are often the best quick wins.

## Suggested Implementation Order

Split into:

### Quick Wins
High-value findings that are XS/S effort.

### Next
Important M-effort product improvements.

### Later
Lower-confidence or architectural changes.

---

# Optional Fix Mode

If the user asks to improve/fix the app rather than only audit it:

1. Complete the audit first.
2. Present or internally establish the prioritized findings.
3. Start with high-confidence P0/P1 findings and high-value quick wins.
4. Make one coherent group of changes at a time.
5. Re-run the affected user journeys after each group.
6. Verify from the UI, not merely from tests/source code.
7. Do not silently implement low-confidence feature ideas.
8. Preserve existing behavior unless the improvement requires changing it.

After fixes, report:

```text
Fixed
- P-004 Sync All
- A-007 Remove custom path
- R-002 Retry failed sync

Still open
- P-009 Retention policies
- C-003 Bulk snapshot deletion

Rejected / not recommended
- P-012 Auto-delete all old snapshots
  Reason: destructive default with weak user benefit
```

---

# agent-browser Guidance

`agent-browser` is mandatory for this skill, not optional.

Use the `agent-browser` CLI to operate the actual running application throughout
the user-facing portions of the audit.

Typical interaction loop:

```bash
agent-browser open <app-url>
agent-browser snapshot -i
agent-browser click <target>
agent-browser snapshot -i
```

Use the syntax supported by the installed `agent-browser` version when it
differs from these examples.

Guidelines:

- Prefer accessible/semantic targets exposed by the interactive snapshot.
- Navigate through the visible UI instead of calling internal APIs to shortcut
  flows.
- Fill forms as a user would.
- Trigger actions through visible controls.
- Re-snapshot after meaningful actions.
- Verify actions by observing resulting browser state.
- Refresh and revisit pages to detect persistence problems.
- Use browser back/forward where relevant.
- Exercise empty, populated, loading, error, and recovery states when reachable.
- Take screenshots when they materially document a finding.
- Restart or reopen the application for persistence/recovery checks when
  practical.
- Inspect console/network information only after first observing the user-facing
  symptom, if the installed `agent-browser` capabilities expose that information.
- Do not infer success merely because an underlying request succeeded.
- Do not treat an element's existence as proof that the workflow works; execute
  the workflow.
- Do not use implementation knowledge to choose hidden or non-obvious UI paths
  during the cold-use pass.
- Keep browser commands user-oriented. Do not use browser scripting as a covert
  substitute for operating the product normally.

If `agent-browser` loses access to the application during the audit and cannot
recover, stop the audit and tell the user what failed. Do not complete the
remaining audit using source inspection as a substitute.

If only one portion of the application is inaccessible while `agent-browser`
itself continues to work, record that specific limitation and continue only
with the parts that can genuinely be exercised.

---

# Anti-Patterns

Do not produce generic comments such as:

- "Improve the UX."
- "Make the UI more intuitive."
- "Add more feedback."
- "Consider bulk actions."

Instead identify the exact workflow, observed problem, user expectation, and
smallest useful solution.

Do not:

- invent features without a user goal
- assume every entity needs full CRUD
- recommend bulk destructive actions casually
- prioritize visual polish above missing core capabilities
- inspect source before the cold-use pass
- continue the audit when `agent-browser` is unavailable or unusable
- replace `agent-browser` dogfooding with static review, raw HTTP calls, source inspection, or another browser driver
- assign browser dogfooding to a subagent that cannot actually use `agent-browser`
- let subagents inspect source during their cold-use browser lanes
- prime subagents with the missing feature you expect them to discover
- blindly merge duplicate or contradictory subagent findings
- mark assumptions as observations
- treat workarounds as proof a product gap does not exist
- stop after the happy path
- equate technical completion with understandable user completion

---

# Product-Thinking Heuristics

Keep these questions active throughout the audit:

> What would I try next if nobody explained this app to me?

> What obvious action is missing from the place where I need it?

> Am I repeating something the product could do once?

> If I can add it, how do I get rid of it?

> If I can override it, how do I return to automatic/default?

> If this fails, what can I do now?

> If there are twenty of these, does this workflow still make sense?

> If I come back tomorrow, can I understand what happened?

> Is the product exposing its implementation model instead of the user's mental model?

> Does the UI expose everything the underlying product already knows how to do?

> Is this actually a feature gap, or am I inventing scope?

The objective is not to find the most issues.

The objective is to find the product gaps that a real user would notice and
that meaningfully improve the product when fixed.
