package tui

import (
	"context"
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"unknwon.dev/kargo-tui/internal/kargo"
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
func (m Model) updateContextPicker(ctx context.Context, msg tea.Msg) (tea.Model, tea.Cmd) {
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

	case contextSwitchedMsg:
		m.ctxLoggingIn = false
		m.ctxLoginStatus = ""
		m.ctxLoginCancel = nil
		if msg.err != nil {
			m.ctxError = msg.err
			return m, nil
		}
		// Apply the freshly-loaded state in one atomic transition so
		// the model never renders a half-switched view. The old
		// stage-watch goroutine is stopped here (restartStageWatch
		// cancels any in-flight watch before starting a fresh one
		// against the new client + project).
		m.client = msg.client
		m.contextName = msg.name
		m.project = msg.defaultProject
		m.deploys = nil
		m.freights = nil
		m.visibleDeploys = nil
		m.visibleFreights = nil
		m.lastDeployRows = nil
		m.lastFreightRows = nil
		m.deploysError = nil
		m.freightsError = nil
		// Clear the stale "unauthenticated" error that the previous
		// failing load left painted in the project picker, and drop the
		// "session expired" banner: contextSwitchCmd just exercised the
		// new bearer to discover shards and list projects, so we know
		// the new token works.
		m.projectsError = nil
		m.noteAuthSuccess()
		m.argoShards = msg.shards
		if m.argoShardsCache == nil {
			m.argoShardsCache = make(map[string]kargo.ArgoCDShards)
		}
		// Only cache a non-empty discovery. An empty result usually means
		// discovery failed or timed out on a cold connection. Caching it
		// would block re-discovery on every later switch to this context
		// and leave argo links missing until a full restart.
		if len(msg.shards) > 0 {
			m.argoShardsCache[msg.name] = msg.shards
		}
		if msg.defaultProject != "" {
			m.phase = phaseRunning
			// Re-fit tables in case the WindowSizeMsg already landed
			// during the picker phase (the picker handlers only store
			// width/height on the model and skip table.SetHeight, so
			// without this the running view inherits the bubbles default
			// table size and renders zero visible rows).
			m.fitTablesToWindow()
			m.refreshRows(ctx)
			m.refreshPanel()
			m.restartStageWatch()
			m.loading = true
			return m, tea.Batch(
				loadDeploysCmd(msg.client, msg.defaultProject),
				loadFreightsCmd(msg.client, msg.defaultProject),
				tickCmd(),
			)
		}
		// Multiple (or zero) projects: drop the user into the project
		// picker so they can choose. projectList from the cmd is
		// non-nil only when ListProjects succeeded; on failure we let
		// loadProjectsCmd retry from the picker the way it always has.
		m.phase = phasePickingProject
		m.nsLoading = msg.projectList == nil
		m.nsCursor = 0
		m.nsExplicit = true
		m.nsFilter.SetValue("")
		m.nsFilter.Focus()
		if msg.projectList != nil {
			m.projects = msg.projectList
			m.nsLoading = false
			return m, nil
		}
		return m, loadProjectsCmd(msg.client)

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

		// While a delete confirmation is up, only y/n/esc are live so a
		// stray keystroke can't both dismiss the prompt and leak into the
		// filter input.
		if m.ctxDeleting != "" {
			switch key {
			case "y":
				name := m.ctxDeleting
				m.ctxDeleting = ""
				if m.ctxDelete == nil {
					return m, nil
				}
				if err := m.ctxDelete(name); err != nil {
					m.ctxError = err
					return m, nil
				}
				m.ctxError = nil
				if i := slices.Index(m.ctxNames, name); i >= 0 {
					m.ctxNames = slices.Delete(m.ctxNames, i, i+1)
				}
				if m.ctxCursor >= len(m.filteredContexts()) {
					m.ctxCursor = len(m.filteredContexts()) - 1
				}
				if m.ctxCursor < 0 {
					m.ctxCursor = 0
				}
				return m, nil
			case "n", "esc":
				m.ctxDeleting = ""
				return m, nil
			}
			return m, nil
		}

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
		case "D":
			// Arm the inline delete confirmation for the highlighted
			// context. Removal happens on `y`.
			if m.ctxDelete == nil || m.ctxCursor < 0 || m.ctxCursor >= len(filtered) {
				return m, nil
			}
			m.ctxDeleting = filtered[m.ctxCursor]
			m.ctxError = nil
			return m, nil
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
	// Stay on the context picker with a progress hint while the
	// blocking work runs on a goroutine. The picker's existing
	// ctxLoggingIn / ctxLoginStatus rendering is reused so the user
	// sees "Switching to …" instead of a frozen UI. Reducer transitions
	// fully into the new context only when contextSwitchedMsg arrives,
	// so a mid-switch panic or cancellation can't leave the model
	// half-rebuilt against the old client.
	m.phase = phasePickingContext
	m.ctxLoggingIn = true
	m.ctxAdding = false
	m.ctxError = nil
	m.ctxLoginStatus = "Switching to " + name + "…"
	send := m.ctxSend
	cached := m.argoShardsCache[name]
	return m, contextSwitchCmd(m.ctxBuilder, name, cached, send)
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
		lines = append(lines, hintStyle.Render(wrap("type to filter · ↑/↓ select · enter switch · + add · D delete · esc cancel", innerW)))
		lines = append(lines, "")
		filterRow = lipgloss.Height(strings.Join(lines, "\n"))
		lines = append(lines, m.ctxFilter.View())
		lines = append(lines, "")
		if m.ctxDeleting != "" {
			lines = append(lines, errStyle.Render(wrap("delete context \""+m.ctxDeleting+"\"? press y to confirm, n to cancel", innerW)), "")
		}
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
