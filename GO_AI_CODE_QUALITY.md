# Go AI Code Quality Guardrails

Use this file as the implementation brief for adding strict, practical Go linting and AI-code-quality guardrails to this repository.

## Objective

Add a Go quality gate that catches common AI-generated code problems without forcing pointless style ceremony.

The goal is to prevent:

- ignored errors
- incorrect nil/error handling
- dead helpers and unused abstractions
- unnecessary interfaces
- duplicated implementation
- unnecessary dependencies
- excessive nesting and complexity
- broken context propagation
- leaked HTTP response bodies
- placeholder TODO/FIXME code
- unjustified `//nolint`
- common security mistakes
- speculative abstractions and dependency creep

Do **not** blindly enable every available linter.

The lint setup should improve correctness and maintainability without encouraging code to be split into artificial helpers purely to satisfy metrics.

---

## Required tooling

Use:

- `golangci-lint` v2
- `gofumpt`
- `goimports`
- normal Go tests
- race detector where practical

Create or update `.golangci.yml` using the following baseline.

```yaml
version: "2"

linters:
  default: none

  enable:
    # Core correctness
    - govet
    - staticcheck
    - errcheck
    - ineffassign
    - unused

    # Error handling
    - errorlint
    - nilerr
    - nilnesserr
    - nilnil

    # AI abstraction/slop control
    - iface
    - interfacebloat
    - unparam
    - dupl
    - gocritic
    - revive

    # Context/resource correctness
    - contextcheck
    - noctx
    - fatcontext
    - bodyclose

    # Logic correctness
    - exhaustive
    - copyloopvar
    - durationcheck

    # Maintainability
    - gocognit
    - nestif

    # Repository hygiene
    - godox
    - nolintlint
    - forbidigo

    # Security
    - gosec

  settings:
    errcheck:
      check-type-assertions: true
      check-blank: true

    dupl:
      threshold: 100

    interfacebloat:
      max: 5

    gocognit:
      min-complexity: 20

    nestif:
      min-complexity: 5

    godox:
      keywords:
        - TODO
        - FIXME
        - HACK
        - XXX

    forbidigo:
      forbid:
        - pattern: '^fmt\.Print.*$'
          msg: "Do not commit debug printing."
        - pattern: '^print(ln)?$'
          msg: "Do not commit debug printing."

  exclusions:
    warn-unused: true

    rules:
      - path: '_test\.go'
        linters:
          - dupl
          - gosec

formatters:
  enable:
    - gofumpt
    - goimports
```

If a listed linter is unavailable in the exact installed `golangci-lint` version, verify the current v2 equivalent and use the closest supported rule instead of silently dropping the check.

---

## AI-specific coding rules

These are repository rules, not merely linter rules.

### Prefer deletion over abstraction

Do not introduce:

- an interface with one implementation unless it represents a real boundary
- a wrapper around a single function without a concrete reason
- a helper used exactly once unless it materially simplifies the caller
- a configuration option without an immediate requirement
- a generic abstraction for hypothetical future use
- a new package for only one or two trivial operations
- compatibility or fallback behavior that was not requested

Prefer the smallest direct implementation that satisfies the current requirement.

---

## Interface rules

Avoid Java-style implementation naming and unnecessary interface layers.

Bad:

```go
type UserRepository interface {
    GetUser(...)
}

type UserRepositoryImpl struct {
    ...
}

func NewUserRepository() UserRepository {
    return &UserRepositoryImpl{}
}
```

Prefer concrete types unless an interface is actually needed.

When an interface is required:

- define it near the consumer when practical
- keep it small
- avoid creating it solely for mocking
- avoid `SomethingImpl` naming

Do not add an interface only because another implementation might exist someday.

---

## Error handling rules

Every returned error must be intentionally handled.

Avoid:

```go
result, _ := doSomething()
```

Avoid unchecked type assertions:

```go
v := x.(SomeType)
```

unless a panic is explicitly intended.

Prefer:

```go
v, ok := x.(SomeType)
if !ok {
    ...
}
```

Error wrapping should add meaningful context.

Good:

```go
return fmt.Errorf("upload save %q: %w", saveID, err)
```

Bad:

```go
return fmt.Errorf("failed to execute operation: %w", err)
```

Do not mechanically wrap errors at every layer.

Do not enable `wrapcheck` or `err113` globally unless the repository has a specific reason to enforce them.

---

## Context rules

For operations that perform I/O or may block:

- accept or propagate `context.Context`
- do not create `context.Background()` deep inside request paths
- do not store contexts in structs unless there is a strong lifecycle reason
- propagate cancellation into HTTP, DB, filesystem, and remote operations where supported
- do not discard a caller's context

---

## Dependency rules

Do not add a third-party dependency when the Go standard library already solves the problem reasonably well.

Before adding a dependency:

1. check whether the standard library is sufficient
2. check whether an existing repository dependency already provides the needed functionality
3. justify the new dependency in the implementation summary

Do not modify `go.mod` or `go.sum` for speculative convenience.

If the requested change does not require dependency changes, CI should verify:

```bash
git diff --exit-code go.mod go.sum
```

If dependency changes are intentional, explain why they are needed.

Consider adding `depguard` or `gomodguard_v2` configuration for repository-specific dependency restrictions.

---

## Placeholder-code rules

Do not commit:

- TODO
- FIXME
- HACK
- XXX
- placeholder implementations
- "implement later" branches
- speculative comments
- comments that merely restate obvious code

Bad:

```go
// Loop through all games.
for _, game := range games {
```

Comments should explain **why**, constraints, edge cases, invariants, or non-obvious behavior.

---

## `nolint` rules

Do not use broad exclusions such as:

```go
//nolint
```

A suppression must:

- name the exact linter
- include a concrete reason

Example:

```go
//nolint:gosec // path is generated internally and cannot contain user input.
```

Do not suppress a linter when the underlying issue can reasonably be fixed.

---

## Complexity rules

Complexity checks are guardrails, not design targets.

Do not split a cohesive function into multiple meaningless one-use helpers just to satisfy a metric.

Bad result:

```go
func syncGame() {
    prepareSync()
    resolveSync()
    executeSync()
    finalizeSync()
}
```

when those helpers exist only to reduce function length or complexity scores.

Refactor when it creates a real conceptual boundary, reusable operation, clearer invariant, or independently testable behavior.

Use approximately:

```yaml
gocognit:
  min-complexity: 20
```

as a warning ceiling rather than trying to force every function below an arbitrary low threshold.

---

## Linters intentionally not enabled by default

Do not enable the following globally unless there is a repository-specific reason:

- `varnamelen`
- `lll`
- `godot`
- `exhaustruct`
- `gochecknoglobals`
- `funlen`
- `nonamedreturns`
- `mnd`
- `wsl_v5`
- `ireturn`
- `wrapcheck`
- `err113`

These can create ceremony or encourage worse AI-generated refactors.

---

## Formatting

Formatting must be deterministic.

Run:

```bash
golangci-lint fmt
```

or the equivalent repository commands using:

- `gofumpt`
- `goimports`

Formatting failures should be CI failures.

---

## Required validation commands

The final implementation should expose a simple repository command, script, or Make target that performs the equivalent of:

```bash
go mod tidy

golangci-lint fmt --diff
golangci-lint run ./...

go test ./...
go test -race ./...

git diff --check
```

If `go mod tidy` changes `go.mod` or `go.sum` unexpectedly, treat that as a failure unless dependency changes are part of the task.

Where practical also run:

```bash
go test -cover ./...
```

Do not invent a coverage threshold unless the repository already has one.

---

## Suggested Make targets

If the repository already uses `make`, prefer adding something similar to:

```make
fmt:
	golangci-lint fmt

lint:
	golangci-lint run ./...

test:
	go test ./...

test-race:
	go test -race ./...

check: lint test test-race
	git diff --check
```

Adapt this to the repository's existing build tooling rather than introducing `make` solely for these commands.

---

## CI requirements

Add or update CI so that pull requests fail when:

- formatting differs
- linting fails
- tests fail
- race tests fail, if the repository supports them reliably
- invalid `//nolint` directives are present
- placeholder TODO/FIXME/HACK/XXX markers are committed
- unintended module dependency changes occur

Do not duplicate an existing CI pipeline if the repository already has equivalent jobs. Extend the current workflow instead.

---

## Agent behavior

When implementing this task:

1. inspect the existing repository structure and tooling first
2. preserve existing conventions where they are sensible
3. avoid adding tooling that duplicates existing tooling
4. add the smallest configuration required
5. do not refactor unrelated application code merely to make lint pass unless the lint finding is a genuine correctness issue
6. if existing code produces many historical lint failures, establish a practical migration strategy rather than disabling valuable checks globally
7. prefer narrow path-based exclusions over globally disabling a linter
8. run the complete validation suite before considering the task complete

---

## Completion criteria

The task is complete when:

- `.golangci.yml` or equivalent exists and is valid for `golangci-lint` v2
- formatter configuration is active
- the selected linters run successfully
- repository-specific exclusions are minimal and justified
- CI or the repository's validation command runs lint + tests
- no new unnecessary abstraction or dependency was introduced
- all validation commands pass
- the implementation summary explains any intentional exclusions or deviations from this document
