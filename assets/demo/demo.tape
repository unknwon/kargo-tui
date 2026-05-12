# kargo-tui comprehensive demo against the local mock server.
#
# Re-render:
#   ./bin/kargo-mock-server &     # leave running in the background
#   vhs assets/demo/demo.tape
# Output lands at assets/demo/demo.gif.

Output assets/demo/demo.gif

Set Width 1600
Set Height 900
Set FontSize 13
Set Framerate 24
Set Theme "Catppuccin Mocha"
Set PlaybackSpeed 1.0

# ============================================================
# 1. Boot
# ============================================================
Type "./bin/kargo-tui --context mock"
Enter
Sleep 1500ms

# Project picker visible briefly.
Sleep 1500ms

# Pick acme-commerce.
Enter
Sleep 2s

# ============================================================
# 2. Deploys table — variety of states + LIST FILTER
# ============================================================
Down 6
Sleep 600ms
Down 6
Sleep 600ms
Down 6
Sleep 1s

# List filter — incremental search.
Type "/"
Sleep 300ms
Type "payments"
Sleep 1500ms

# Refine.
Backspace 4
Sleep 400ms
Type "prod"
Sleep 1200ms

# Refine again.
Backspace 4
Sleep 400ms
Type "ap-south"
Sleep 1200ms

# Clear.
Escape
Sleep 600ms
Escape
Sleep 600ms

# ============================================================
# 3. Logs overlay — promotion history + events
# ============================================================
Type "l"
Sleep 2s

Tab
Sleep 2s

Tab
Sleep 1200ms

Escape
Sleep 500ms

# ============================================================
# 4. Tree view — 1-control → many-deploys fan-out
# ============================================================
Type "t"
Sleep 2500ms
Down 8
Sleep 1s
Down 8
Sleep 1500ms

# ============================================================
# 5. Graph view — pan + GRAPH SEARCH (no logs/details here)
# ============================================================
Type "g"
Sleep 2s

# Pan right.
Type "lllllllll"
Sleep 1200ms

# Pan down.
Type "jjjjjjjj"
Sleep 1200ms

# Pan back.
Type "kkkkkkkk"
Sleep 600ms
Type "hhhhhhhhh"
Sleep 1s

# Graph search.
Type "/"
Sleep 300ms
Type "trunk"
Sleep 1200ms
Enter
Sleep 800ms
Type "n"
Sleep 800ms
Type "n"
Sleep 800ms
Type "n"
Sleep 800ms
Escape
Sleep 500ms

# ============================================================
# 6. THE WOW — promote downstream from a trunk
# ============================================================
Type "/"
Sleep 300ms
Type "auth-trunk"
Sleep 1s
Enter
Sleep 800ms
Escape
Sleep 500ms

Type ">"
Sleep 1500ms
Down 3
Sleep 500ms
Enter
Sleep 700ms
Type "y"
Sleep 700ms
Escape
Sleep 8s

# ============================================================
# 7. Switch to acme-edge — different DAG shape
# ============================================================
Type "p"
Sleep 700ms
Down
Sleep 300ms
Enter
Sleep 1500ms

Type "t"
Sleep 2500ms
Down 4
Sleep 1500ms

# ============================================================
# 8. Quit
# ============================================================
Type "q"
Sleep 500ms
