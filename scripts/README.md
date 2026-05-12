# tui-probe: driving kargo-tui from outside the terminal

`scripts/tui-probe.sh` runs the TUI inside a detached `tmux` session,
sends keystrokes into it, and captures the rendered screen as text.
Claude (or any other automation) uses this to exercise the TUI without
needing a visual terminal.

## Why this exists

The TUI is the product: a `bubbletea` alt-screen app with graph
rendering, picker overlays, etc. To verify a change actually works end
to end, we need to launch the binary, press the same keys a human
would, and read the rendered output.

The trick: `tmux capture-pane -p` returns the visible buffer of any
pane as plain text. Unicode box-art, cursor markers, tabs, table
columns — all preserved in their grid positions. We lose color and
font, but for behaviour testing (did the picker open, did the freight
update after promote, did the graph wire stages correctly) that doesn't
matter.

## Prerequisites

```bash
brew install tmux
```

That's it. Pane size is hard-coded to 220x60 inside the script so
captures are deterministic regardless of the host terminal.

## Subcommands

```
start <api-url>       Build ./bin/kargo-tui and launch it in a tmux
                      session named "kargo". The TUI uses the saved
                      Kargo context, so register the target server
                      first with `kargo-tui auth login <url>`.
start-mock            Build ./bin/kargo-mock-server and launch it on
                      :8080 in a tmux session named "mock".
send <keys ...>       Send keystrokes to the kargo session. Uses tmux
                      syntax: literal chars (`p`, `y`) or named keys
                      (`Down`, `Enter`, `Escape`, `Tab`).
capture [session]     Print current pane contents (default: kargo).
                      Pass `mock` to read the mock server's stdout.
wait-text <s> [secs]  Block until <s> appears in the kargo pane or
                      time out (default 5s).
stop [session]        Kill named session (default: kargo).
stop-all              Kill both kargo and mock sessions.
status                List live tmux sessions.
```

## End-to-end smoke test (the full playbook)

This is what I run after touching anything in `cmd/kargo-mock-server/`
or anything in the TUI that affects an RPC. It exercises every RPC
the TUI calls.

```bash
# 0. one-time setup: register mock as a kargo context
go build -o bin/kargo-tui ./cmd/kargo-tui
./bin/kargo-tui auth login http://localhost:8080 --name mock

# 1. boot mock + TUI
./scripts/tui-probe.sh start-mock
./scripts/tui-probe.sh start http://localhost:8080
sleep 2
./scripts/tui-probe.sh capture | head -15
# expect: project picker showing acme-web / acme-platform / acme-mobile

# 2. enter acme-web
./scripts/tui-probe.sh send "Down" "Down" "Enter"
sleep 2
./scripts/tui-probe.sh capture | head -5
# expect: header "kargo-tui · deploys · mock · project=acme-web · 150 items"

# 3. promote on the selected stage (capital P)
./scripts/tui-probe.sh send "P"
sleep 1
./scripts/tui-probe.sh capture | head -10
# expect: "Promote · <stagename>" header, freight list with one row
# marked "current"

# 4. pick a different freight + confirm
./scripts/tui-probe.sh send "Down" "Down" "Enter"
sleep 0.5
./scripts/tui-probe.sh send "y"
sleep 1
./scripts/tui-probe.sh capture | head -8
# expect: "promoted: <stage>.<ULID>.<freight-suffix>"

# 5. dismiss, wait for stream to update the table
./scripts/tui-probe.sh send "Escape"
sleep 5
./scripts/tui-probe.sh capture | sed -n '1,3p;/<stagename>/p'
# expect: that stage's freight column shows the new freight you picked,
# WITHOUT a manual reload — proves WatchStages stream works

# 6. logs/activity view (l, Tab)
./scripts/tui-probe.sh send "l"
sleep 2
./scripts/tui-probe.sh capture | head -15
# expect: "Logs · <stage>" with a Promotions tab showing recent promos
# top entry should be the one you just did, "Xs ago"

./scripts/tui-probe.sh send "Tab"
sleep 1
./scripts/tui-probe.sh capture | head -15
# expect: Events tab with PromotionCreated + PromotionSucceeded entries

# 7. graph view + cascade demo (PromoteDownstream)
./scripts/tui-probe.sh send "Escape"
./scripts/tui-probe.sh send "g"
sleep 1
./scripts/tui-probe.sh capture | head -10
# expect: box-art graph with one node double-bordered (focused)

./scripts/tui-probe.sh send ">"
sleep 1
./scripts/tui-probe.sh send "Enter"
./scripts/tui-probe.sh send "y"
sleep 0.5
./scripts/tui-probe.sh send "Escape"
sleep 7
./scripts/tui-probe.sh capture | head -10
# expect: downstream stage's freight column updates within ~5s as the
# cascade lands

# 8. tree view
./scripts/tui-probe.sh send "t"
sleep 1
./scripts/tui-probe.sh capture | head -20
# expect: ascii-tree rendering with [-]/[+] expand markers, ages

# 9. switch to acme-platform + verify it renders too
./scripts/tui-probe.sh send "p"
./scripts/tui-probe.sh send "Down" "Enter"
sleep 2
./scripts/tui-probe.sh send "g"
sleep 1
./scripts/tui-probe.sh capture | head -10
# expect: "project=acme-platform · 100 stages" header, graph renders

# 10. acme-mobile + hotfix-lane verification
./scripts/tui-probe.sh send "p"
./scripts/tui-probe.sh send "Enter"
sleep 2
./scripts/tui-probe.sh send "t"
sleep 1
./scripts/tui-probe.sh capture | head -10
# expect: tree view shows app-qa with two children: app-hotfix AND
# app-internal (proves the hotfix-from-qa fan-out is wired)

# 11. tear down
./scripts/tui-probe.sh stop-all
```

If all 11 steps print the expected output, the mock + TUI integration
is healthy. Any step that doesn't is a regression.

## Common pitfalls

- **Cursor position changes between runs.** The deploys list is sorted
  newest-first by stage creation. Background motion can change which
  stage you land on if you re-run after several seconds. Reach for a
  named stage by typing into the filter (`/` then text) instead of
  relying on `Down Down Enter` counts.

- **Capital vs lowercase P.** Lowercase `p` opens the project picker.
  Capital `P` is promote. Easy to mix up; the project picker
  short-circuits with "no Kargo projects found" if the mock isn't
  running, which is the canary that you typed the wrong case.

- **Stream takes ~5 seconds after a promote.** The promote engine
  sleeps 1.5s before Pending→Running and another 2.5s before
  Running→Succeeded. If you `capture` immediately after `y` you'll see
  the old freight; sleep at least 5 seconds before asserting the table
  changed.

- **Pane width matters for graph layout.** 220 cols fits 5 stages
  across at the default zoom; 100 cols fits 2. The script pins 220 so
  layout asserts stay stable.

- **Mouse mode interferes with `send-keys` Escape**. The TUI sets
  `KARGO_TUI_DISABLE_MOUSE=1` via the start command to avoid this; if
  you change `tui-probe.sh start` to drop that env var, Escape inside
  an overlay may not register.

## Adding new tests

If you add a new RPC or a new TUI flow, copy a block from the playbook
above and add it. Keep the assertions to "expect: <some string>"
comments rather than exit-code-driven asserts — the goal is for a
human (or LLM) reading the playbook to know what should be on screen
at each step.

## What I can't see this way

- Color. The text grid strips ANSI escape codes by default. Pass
  `tmux capture-pane -e` to keep them, but parsing escapes to assert
  "this node is yellow" is fragile.
- Animation timing. Captures are discrete snapshots. If a frame is
  drawn between two captures, I miss it. For verifying state
  transitions, sleep long enough that the new steady state is reached.
- Font rendering, anti-aliasing, terminal-specific quirks
  (Ligatures, emoji presentation, Powerline glyphs). For these, ask
  a human to paste a screenshot.

## Files referenced

- `scripts/tui-probe.sh` — the script itself.
- `cmd/kargo-mock-server/` — fake Kargo API the TUI talks to during
  these tests.
- `examples/mock/*.yaml` — topology fixtures consumed by the mock.
