#!/usr/bin/env bash
# tui-probe.sh — drive kargo-tui inside a detached tmux session and
# capture the rendered screen as text. Used by Claude to verify the TUI
# end-to-end against the local kargo-mock-server.
#
# Subcommands:
#   start <api-url>           Boot kargo-tui in a tmux session named "kargo"
#                             pointed at <api-url>.
#   start-mock                Boot ./kargo-mock-server in a tmux session
#                             named "mock" (foreground inside the pane).
#   send <keys>               Send keystrokes to the kargo session.
#                             Use tmux syntax: "p", "Enter", "Down Down q".
#   capture [session]         Print the current pane contents (default
#                             session: "kargo").
#   wait-text <text> [secs]   Block until <text> appears in the kargo pane
#                             or timeout (default 5s). Exits 0 on match,
#                             1 on timeout.
#   stop [session]            Kill the named tmux session (default: kargo).
#   stop-all                  Kill both kargo and mock sessions.
#   status                    List live tmux sessions.
#
# Width/height are pinned (220x60) so captures are deterministic regardless
# of the host terminal size.

set -euo pipefail

WIDTH=220
HEIGHT=60
KARGO_SESSION="kargo"
MOCK_SESSION="mock"

die() { echo "tui-probe: $*" >&2; exit 1; }

cmd_start() {
    local url="${1:-}"
    [[ -z "$url" ]] && die "start requires an API URL"
    tmux kill-session -t "$KARGO_SESSION" 2>/dev/null || true
    # Build first so the TUI launches instantly instead of compiling
    # inside the pane (which would race the capture).
    go build -o ./bin/kargo-tui ./cmd/kargo-tui
    tmux new-session -d -s "$KARGO_SESSION" -x "$WIDTH" -y "$HEIGHT" \
        "KARGO_TUI_DISABLE_MOUSE=1 ./bin/kargo-tui"
    echo "started kargo session ($WIDTH x $HEIGHT). url: $url"
}

cmd_start_mock() {
    tmux kill-session -t "$MOCK_SESSION" 2>/dev/null || true
    go build -o ./bin/kargo-mock-server ./cmd/kargo-mock-server
    tmux new-session -d -s "$MOCK_SESSION" -x 120 -y 30 \
        "./bin/kargo-mock-server --addr :8080 --fixtures-dir examples/mock"
    # Give the server a moment to bind and bootstrap.
    sleep 1
    echo "started mock server on :8080"
}

cmd_send() {
    [[ $# -eq 0 ]] && die "send requires at least one key"
    tmux send-keys -t "$KARGO_SESSION" "$@"
}

cmd_capture() {
    local session="${1:-$KARGO_SESSION}"
    tmux capture-pane -t "$session" -p
}

cmd_wait_text() {
    local needle="${1:-}"
    local timeout="${2:-5}"
    [[ -z "$needle" ]] && die "wait-text requires a string"
    local deadline=$(( $(date +%s) + timeout ))
    while (( $(date +%s) < deadline )); do
        if tmux capture-pane -t "$KARGO_SESSION" -p | grep -qF "$needle"; then
            return 0
        fi
        sleep 0.2
    done
    echo "tui-probe: timed out waiting for: $needle" >&2
    tmux capture-pane -t "$KARGO_SESSION" -p >&2
    return 1
}

cmd_stop() {
    local session="${1:-$KARGO_SESSION}"
    tmux kill-session -t "$session" 2>/dev/null || true
}

cmd_stop_all() {
    tmux kill-session -t "$KARGO_SESSION" 2>/dev/null || true
    tmux kill-session -t "$MOCK_SESSION" 2>/dev/null || true
}

cmd_status() {
    tmux list-sessions 2>/dev/null || echo "no tmux sessions"
}

main() {
    local sub="${1:-}"
    shift || true
    case "$sub" in
        start)       cmd_start "$@";;
        start-mock)  cmd_start_mock "$@";;
        send)        cmd_send "$@";;
        capture)     cmd_capture "$@";;
        wait-text)   cmd_wait_text "$@";;
        stop)        cmd_stop "$@";;
        stop-all)    cmd_stop_all;;
        status)      cmd_status;;
        ""|-h|--help)
            sed -n '2,/^set -euo/p' "$0" | sed 's/^# \?//' | sed '$d'
            ;;
        *) die "unknown subcommand: $sub";;
    esac
}

main "$@"
