package tui

import (
	"context"
	"slices"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// updateContextPicker handles input while the context picker is active. It
// is only invoked when the model has a populated ctxBuilder; otherwise the
// `C` keybinding is a no-op and this code path is unreachable.
//
// Three sub-modes share this handler:
//   - Browsing the configured contexts (default).
//   - Typing a new Kargo URL to log in to (`+` opens, esc cancels).
//   - Waiting for the SSO callback to come back (ctxLoggingIn). All keys
//     except ctrl+c are ignored while the browser flow is in progress.
func (m Model) updateContextPicker(msg tea.Msg) (tea.Model, tea.Cmd) {
	// Forward terminal paste events to whichever textinput is focused so
	// users can paste a Kargo URL or filter string with ⌘V/ctrl+v.
	if pm, ok := msg.(tea.PasteMsg); ok {
		var cmd tea.Cmd
		if m.ctxAdding {
			m.ctxURLInput, cmd = m.ctxURLInput.Update(pm)
		} else {
			m.ctxFilter, cmd = m.ctxFilter.Update(pm)
		}
		return m, cmd
	}

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil

	case loginStatusMsg:
		m.ctxLoginStatus = string(msg)
		return m, nil

	case contextLoginMsg:
		m.ctxLoggingIn = false
		m.ctxLoginCancel = nil
		m.ctxLoginStatus = ""
		if msg.err != nil {
			m.ctxError = msg.err
			m.ctxAdding = false
			return m, nil
		}
		// Login succeeded. Refresh the context list and switch to the new
		// context straight away — that's almost always what the user
		// wanted when they typed `+`.
		if !slices.Contains(m.ctxNames, msg.name) {
			m.ctxNames = append(m.ctxNames, msg.name)
		}
		m.ctxAdding = false
		m.ctxURLInput.SetValue("")
		return m.switchContext(msg.name)

	case tea.KeyPressMsg:
		key := msg.String()
		if m.ctxLoggingIn {
			if key == "esc" {
				if m.ctxLoginCancel != nil {
					m.ctxLoginCancel()
				}
				m.ctxLoggingIn = false
				m.ctxLoginCancel = nil
				// Keep ctxAdding at whatever it was when login started:
				// `+` opens the add form so esc returns to it for retry;
				// `R` (inline relogin) opens with ctxAdding=false so esc
				// drops back to the context list instead.
				return m, nil
			}
			return m, nil
		}

		if m.ctxAdding {
			switch key {
			case "esc":
				m.ctxAdding = false
				m.ctxURLInput.SetValue("")
				m.ctxURLInput.Blur()
				return m, nil
			case "enter":
				url := strings.TrimSpace(m.ctxURLInput.Value())
				if url == "" || m.ctxLogin == nil {
					return m, nil
				}
				lctx, cancel := context.WithCancel(context.Background())
				m.ctxLoginCancel = cancel
				m.ctxLoggingIn = true
				m.ctxLoginStatus = "Talking to the Kargo server…"
				m.ctxError = nil
				send := m.ctxSend
				if send == nil {
					send = func(tea.Msg) {}
				}
				return m, runContextLoginCmd(m.ctxLogin, lctx, url, send)
			}
			var cmd tea.Cmd
			m.ctxURLInput, cmd = m.ctxURLInput.Update(msg)
			return m, cmd
		}

		filtered := m.filteredContexts()
		switch key {
		case "ctrl+c":
			return m, tea.Quit
		case "esc":
			m.phase = phaseRunning
			return m, nil
		case "up", "ctrl+p":
			if m.ctxCursor > 0 {
				m.ctxCursor--
			}
			return m, nil
		case "down", "ctrl+n":
			if m.ctxCursor < len(filtered)-1 {
				m.ctxCursor++
			}
			return m, nil
		case "enter":
			if m.ctxCursor < 0 || m.ctxCursor >= len(filtered) {
				return m, nil
			}
			return m.switchContext(filtered[m.ctxCursor])
		case "+":
			// Open the inline "add new instance" subform.
			if m.ctxLogin == nil {
				return m, nil
			}
			m.ctxAdding = true
			m.ctxError = nil
			m.ctxURLInput.SetValue("")
			return m, m.ctxURLInput.Focus()
		}
		var cmd tea.Cmd
		m.ctxFilter, cmd = m.ctxFilter.Update(msg)
		if m.ctxCursor >= len(m.filteredContexts()) {
			m.ctxCursor = 0
		}
		return m, cmd
	}
	return m, nil
}

// filteredContexts returns the configured context names matching the current
// picker filter (case-insensitive substring), or all of them when empty.
func (m Model) filteredContexts() []string {
	q := strings.ToLower(strings.TrimSpace(m.ctxFilter.Value()))
	if q == "" {
		return m.ctxNames
	}
	out := make([]string, 0, len(m.ctxNames))
	for _, n := range m.ctxNames {
		if strings.Contains(strings.ToLower(n), q) {
			out = append(out, n)
		}
	}
	return out
}

// switchContext rebuilds the client against the chosen Kargo context, clears
// loaded data, and routes back through the project picker so the new
// context's projects can be discovered/selected fresh.
func (m Model) switchContext(name string) (Model, tea.Cmd) {
	if m.ctxBuilder == nil {
		m.phase = phaseRunning
		return m, nil
	}
	client, defaultProject, err := m.ctxBuilder(name)
	if err != nil {
		m.ctxError = err
		return m, nil
	}
	m.client = client
	m.contextName = name
	m.project = defaultProject
	m.deploys = nil
	m.freights = nil
	m.visibleDeploys = nil
	m.visibleFreights = nil
	m.lastDeployRows = nil
	m.lastFreightRows = nil
	m.deploysError = nil
	m.freightsError = nil
	m.argoShards = nil

	if defaultProject != "" {
		client.SetProject(defaultProject)
		m.phase = phaseRunning
		m.refreshRows()
		m.refreshPanel()
		m.restartStageWatch()
		m.loading = true
		return m, tea.Batch(
			loadDeploysCmd(client, defaultProject),
			loadFreightsCmd(client, defaultProject),
			discoverArgoShardsCmd(client),
			tickCmd(),
		)
	}

	m.phase = phasePickingProject
	m.nsLoading = true
	m.nsCursor = 0
	m.nsExplicit = true
	m.nsFilter.SetValue("")
	m.nsFilter.Focus()
	return m, tea.Batch(loadProjectsCmd(client), discoverArgoShardsCmd(client), textinput.Blink)
}

// ctxBodyHeight returns the row budget the context picker uses for its
// scrollable list. Mirrors the chrome-line math in contextPickerView so
// the scroll recompute in Update agrees with the renderer. The "Switch"
// branch always reserves 5 lines (title, hint, blank, filter, blank)
// before any list items, plus 2 if an error is shown.
func (m Model) ctxBodyHeight() int {
	chrome := 5
	if m.ctxError != nil {
		chrome += 2
	}
	maxItems := m.height - chrome - 4
	if maxItems < 5 {
		maxItems = 5
	}
	return maxItems
}

// contextPickerView renders the context picker overlay (phasePickingContext).
func (m Model) contextPickerView() tea.View {
	titleStyle := lipgloss.NewStyle().Foreground(normal).Bold(true).Background(bg)
	hintStyle := lipgloss.NewStyle().Foreground(muted).Background(bg)
	itemStyle := lipgloss.NewStyle().Foreground(normal).Background(bg)
	selStyle := lipgloss.NewStyle().Foreground(darkFg).Background(selected).Bold(true)
	errStyle := lipgloss.NewStyle().Foreground(degraded).Background(bg)

	innerW := popupInnerWidth(m.width)

	var lines []string

	// filterRow tracks the body-row index of the active text input (URL
	// input when adding, ctxFilter otherwise) so we can offset the real
	// terminal cursor onto it.
	filterRow := -1

	if m.ctxLoggingIn {
		lines = append(lines, titleStyle.Render("Signing in…"))
		if m.ctxLoginStatus != "" {
			lines = append(lines, itemStyle.Render(wrap(m.ctxLoginStatus, innerW)))
		}
		lines = append(lines, "")
		lines = append(lines, hintStyle.Render("esc to cancel"))
	} else if m.ctxAdding {
		lines = append(lines, titleStyle.Render("Add Kargo context"))
		lines = append(lines, hintStyle.Render(wrap("type the API URL · enter to start SSO · esc cancel", innerW)))
		lines = append(lines, "")
		filterRow = lipgloss.Height(strings.Join(lines, "\n"))
		lines = append(lines, m.ctxURLInput.View())
		if m.ctxError != nil {
			lines = append(lines, "", errStyle.Render(wrap("error: "+m.ctxError.Error(), innerW)))
		}
	} else {
		lines = append(lines, titleStyle.Render("Switch Kargo context"))
		lines = append(lines, hintStyle.Render(wrap("type to filter · ↑/↓ select · enter switch · press + to paste a Kargo URL · esc cancel", innerW)))
		lines = append(lines, "")
		filterRow = lipgloss.Height(strings.Join(lines, "\n"))
		lines = append(lines, m.ctxFilter.View())
		lines = append(lines, "")
		if m.ctxError != nil {
			lines = append(lines, errStyle.Render(wrap("error: "+m.ctxError.Error(), innerW)), "")
		}
		filtered := m.filteredContexts()
		if len(filtered) == 0 {
			if len(m.ctxNames) == 0 {
				lines = append(lines, hintStyle.Render(wrap("no contexts configured · press + and paste a Kargo URL", innerW)))
			} else {
				lines = append(lines, hintStyle.Render("no contexts match this filter"))
			}
		} else {
			maxItems := m.height - len(lines) - 4
			if maxItems < 5 {
				maxItems = 5
			}
			start := clampListScroll(m.ctxScroll, m.ctxCursor, maxItems, len(filtered))
			end := start + maxItems
			if end > len(filtered) {
				end = len(filtered)
			}
			for i := start; i < end; i++ {
				name := filtered[i]
				marker := "  "
				label := name
				if name == m.contextName {
					label += "  (current)"
				}
				label = wrapIndent(label, innerW-2, "  ")
				if i == m.ctxCursor {
					marker = "▌ "
					lines = append(lines, selStyle.Render(marker+label))
				} else {
					lines = append(lines, itemStyle.Render(marker+label))
				}
			}
		}
	}

	body := strings.Join(lines, "\n")
	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(muted).
		Background(bg).
		Padding(1, 2).
		Width(innerW).
		Render(body)
	offX, offY := centerPopupOffsets(box, m.width, m.height)
	box = centerPopup(box, m.width, m.height)
	box = paintFrame(box, m.width, m.height)

	v := tea.NewView(box)
	v.AltScreen = true
	v.BackgroundColor = bg
	v.MouseMode = m.activeMouseMode()
	if filterRow >= 0 {
		var c *tea.Cursor
		if m.ctxAdding {
			c = m.ctxURLInput.Cursor()
		} else {
			c = m.ctxFilter.Cursor()
		}
		if c != nil {
			// Box: border (1) + Padding(1,2). The extra (offX, offY)
			// shift lands the cursor inside the centered popup.
			c.X += offX + 3
			c.Y += offY + 2 + filterRow
			v.Cursor = c
		}
	}
	return v
}
