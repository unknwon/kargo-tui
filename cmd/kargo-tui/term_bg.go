package main

import (
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/x/term"
	"github.com/cockroachdb/errors"
)

// detectTerminalBackgroundDark issues OSC 11 against the controlling tty
// to ask the terminal for its current default background color and
// returns whether that color is dark.
//
// This runs *before* the TUI takes over the screen: once bubbletea's
// renderer writes its own OSC 11 to set the view background, the
// terminal will echo that value back instead of its native theme. So
// detection has to happen here, in cooked-mode main, not inside the
// Bubble Tea model's Init.
//
// Returns (isDark=true, ok=false) when detection fails for any reason
// (no tty, terminal didn't reply within the timeout, malformed reply).
// Callers use ok to decide whether to honor the detection or fall back
// to a configured default.
func detectTerminalBackgroundDark() (isDark bool, ok bool) {
	f, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return true, false
	}
	defer f.Close()

	fd := f.Fd()
	if !term.IsTerminal(fd) {
		return true, false
	}

	// Put the tty in raw mode briefly so the reply isn't line-buffered
	// or echoed. Restore on the way out.
	prev, err := term.MakeRaw(fd)
	if err != nil {
		return true, false
	}
	defer func() { _ = term.Restore(fd, prev) }()

	// OSC 11 query. The terminal answers with the BEL- or ST-terminated
	// form: ESC ] 11 ; rgb:RRRR/GGGG/BBBB BEL  (or ESC \).
	if _, err := f.WriteString("\x1b]11;?\x07"); err != nil {
		return true, false
	}

	// /dev/tty doesn't support SetReadDeadline on macOS (the underlying
	// fd isn't pollable). Race the blocking read against a timer and
	// close the fd to abort the read if the terminal never answers
	// (e.g. tmux without passthrough, ssh into a barebones tty). The
	// reply has to be consumed here either way — leaving it in the
	// input buffer would echo to the shell after we exit.
	done := make(chan []byte, 1)
	go func() {
		buf := make([]byte, 64)
		var got []byte
		for {
			n, err := f.Read(buf)
			if n > 0 {
				got = append(got, buf[:n]...)
				if strings.ContainsAny(string(got), "\x07") || strings.Contains(string(got), "\x1b\\") {
					done <- got
					return
				}
			}
			if err != nil || len(got) > 256 {
				done <- got
				return
			}
		}
	}()

	var got []byte
	select {
	case got = <-done:
	case <-time.After(200 * time.Millisecond):
		_ = f.Close()
		<-done
		return true, false
	}

	r, g, b, perr := parseOSC11Reply(string(got))
	if perr != nil {
		return true, false
	}
	// Standard relative-luminance check, identical to the one ultraviolet
	// uses internally: convert 8-bit RGB to linear, take the perceptual
	// luma, dark if < 0.5.
	luma := (0.299*float64(r) + 0.587*float64(g) + 0.114*float64(b)) / 255.0
	return luma < 0.5, true
}

// parseOSC11Reply extracts R, G, B (8-bit channels) from a terminal's
// OSC 11 response. Accepts both BEL- and ST-terminated replies and the
// hex-component widths terminals actually emit (4-digit common, also 2-
// and 8-digit per the XParseColor grammar).
func parseOSC11Reply(s string) (r, g, b uint8, err error) {
	idx := strings.Index(s, "rgb:")
	if idx < 0 {
		return 0, 0, 0, errors.Newf("no rgb: prefix in %q", s)
	}
	tail := s[idx+len("rgb:"):]
	// Cut off at the terminator.
	for _, term := range []string{"\x07", "\x1b\\"} {
		if i := strings.Index(tail, term); i >= 0 {
			tail = tail[:i]
		}
	}
	parts := strings.Split(strings.TrimSpace(tail), "/")
	if len(parts) != 3 {
		return 0, 0, 0, errors.Newf("want 3 components, got %d in %q", len(parts), tail)
	}
	parseChan := func(p string) (uint8, error) {
		if p == "" {
			return 0, errors.New("empty channel")
		}
		v, err := strconv.ParseUint(p, 16, 32)
		if err != nil {
			return 0, errors.Wrapf(err, "parse %q", p)
		}
		// Channels can be 1–4 hex digits. Scale down to 8-bit.
		switch len(p) {
		case 1:
			v = (v << 4) | v
		case 2:
			// already 8-bit
		case 3:
			v >>= 4
		case 4:
			v >>= 8
		default:
			v >>= (4 * (len(p) - 2))
		}
		return uint8(v), nil
	}
	rv, err := parseChan(parts[0])
	if err != nil {
		return 0, 0, 0, err
	}
	gv, err := parseChan(parts[1])
	if err != nil {
		return 0, 0, 0, err
	}
	bv, err := parseChan(parts[2])
	if err != nil {
		return 0, 0, 0, err
	}
	return rv, gv, bv, nil
}
