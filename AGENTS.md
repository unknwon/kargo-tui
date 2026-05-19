## Core principles

- Stop telling me "You're right", it just shows how incompetent you are. Do it right on your first try, fact-check and review after changes. If you are not sure, ask for help.
- When you see changes made outside your knowledge, use the current version as your new starting point. Do not blindly overwrite those changes or you suck. Even if you have to update the code, always respect the pattern in the surrounding context!

## Style and mechanics

This applies to all texts, including but not limited to UI, documentation, code comments.

- Use sentence case. Preserve original casing for brand names, e.g., Git, GitHub, Go, Git LFS, NGINX.
- End with a period for a full sentence.
- Never use em dashes (`—`) or en dashes (`–`). Rewrite the sentence with a comma, period, colon, or parentheses instead.
- Do not overuse semicolons. Two short sentences are almost always clearer than one sentence joined by a semicolon. Reserve the semicolon for the rare case where the two clauses are so tightly coupled that splitting them loses meaning, never as a default em-dash replacement or a way to chain related thoughts.
- Do not add comments that repeat what the code is doing, always prefer more descriptive names. Do add comments for intentions that aren't obvious via reading the code alone. This rule takes precedence over matching existing patterns.

## Coding guidelines

- Use `github.com/cockroachdb/errors` for error handling.
    - Always wrap errors with context using `errors.Wrap` or `errors.Wrapf`. Do not return bare errors.
- Use `github.com/stretchr/testify` for assertions in tests. Be mindful about the choice of `require` and `assert`, the former should be used when the test cannot proceed meaningfully after a failed assertion.
- Prefer grouping related cases as `t.Run` subtests under a single top-level `TestXxx` function, instead of writing one top-level `TestXxx_Case` per case. Reserve separate top-level functions for unrelated subjects and complex subtest setup.
- Always use backtick (raw string) literals for multi-line strings. Never use `"...\n" +` concatenation.
- When each is available, the first set of arguments should always be: `context.Context`, `*logx.Logger`.
    - Logger scoping (`logger.Scoped(...)`) is done at the call site, not inside the callee's constructor. The constructor stores the logger as-is.
    - Log scope and attribute names use camelCase while respecting Go idioms, e.g., `"userID"`, `"messageLength"`.

## TUI render correctness

- Any "skip rebuild when nothing changed" check (the table refresh in `internal/tui/table.go`, similar caches elsewhere) must compare the rendered output, not source structs. Struct-field equality drifts the moment a cell renderer reads anything new: time-dependent values like `ageString(Created)`, cross-list lookups like `aliasOf` against `m.freights`, or a newly added column will all silently bypass an enumerated field check while still affecting what the user sees. Compare `[]table.Row` against the previous `[]table.Row`, or invalidate a cache via a monotonically bumped version that fires on every data load. If you must compare structs, prove that every input to every cell on the row is covered, and add a regression case before merging.

## UX consistency

- All popup overlays (project picker, context picker, promote confirm, freight picker, etc.) must share the same placement behavior: centered when the terminal is large enough to fit the box, anchored to the top-left when it isn't. Use the `centerPopup` and `centerPopupOffsets` helpers in `internal/tui/cells.go`. Top-left fallback keeps content readable on tiny terminals instead of pushing it off-screen.
- Full-screen frames (the main tree/graph/list views, the Logs and Diff overlays, the keybindings help, the panic popup) are not popups and stay full-screen, not centered.
- When a popup hosts a focused text input, remember to add the centering `(offsetX, offsetY)` to the cursor coordinates so the real cursor tracks the centered box.

## Build instructions

- Prefer `moon` command over vanilla `go` command when available.
- Run `moon run :lint` after every time you finish changing code, and fix all linter errors.
- Run `go mod tidy` after every time you change `go.mod`, do not manually edit `go.sum` file.

## Tool-use guidance

- Use `gh` CLI to access information on github.com that is not publicly available. For public information, directly fetch the webpage and extract content to save GitHub API usage.

## Planning and issue triage

- For multi-issue reviews (code review, security review, refactor lists), always present the findings as a numbered list first and walk through them with the user one by one. The user decides fix or skip per item before any edit.
- For non-trivial changes that touch multiple files, propose the approach (in a plan or in chat) and wait for approval before editing. Do not start edits while the user is still discussing the approach.
- Do not batch-fix multiple items just because the next one seems obvious. Triage strictly item by item.

## Source code control

- Never commit on the `main` branch directly unless being explicitly asked to do so. A single ask only grants a single commit action on the `main` branch.
- Never amend commits unless being explicitly asked to do so.
