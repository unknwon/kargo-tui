package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// updatePicker handles input while the project picker is active. It owns
// keypress, projects-loaded, and window-size events for the picker phase.
func (m Model) updatePicker(msg tea.Msg) (tea.Model, tea.Cmd) {
	if pm, ok := msg.(tea.PasteMsg); ok {
		var cmd tea.Cmd
		m.nsFilter, cmd = m.nsFilter.Update(pm)
		return m, cmd
	}

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil

	case projectsLoadedMsg:
		m.nsLoading = false
		m.projects = msg.projects
		m.projectsError = msg.err
		// Auto-select on initial startup only — when the user explicitly
		// invoked the picker mid-session, always show the list even if it
		// contains a single entry.
		if !m.nsExplicit && msg.err == nil && len(msg.projects) == 1 {
			return m.startWithProject(msg.projects[0])
		}
		return m, nil

	case tea.KeyPressMsg:
		key := msg.String()
		filtered := m.filteredProjects()
		switch key {
		case "ctrl+c":
			return m, tea.Quit
		case "esc":
			// If picker was opened from a running session, allow esc back out.
			if m.project != "" {
				m.phase = phaseRunning
				return m, nil
			}
			return m, tea.Quit
		case "up", "ctrl+p":
			if m.nsCursor > 0 {
				m.nsCursor--
			}
			return m, nil
		case "down", "ctrl+n":
			if m.nsCursor < len(filtered)-1 {
				m.nsCursor++
			}
			return m, nil
		case "enter":
			if m.nsCursor < 0 || m.nsCursor >= len(filtered) {
				return m, nil
			}
			return m.startWithProject(filtered[m.nsCursor])
		case "r":
			if !m.nsLoading {
				m.nsLoading = true
				return m, loadProjectsCmd(m.client)
			}
			return m, nil
		}
		var cmd tea.Cmd
		m.nsFilter, cmd = m.nsFilter.Update(msg)
		// Reset cursor when the filter narrows to fewer rows.
		if m.nsCursor >= len(m.filteredProjects()) {
			m.nsCursor = 0
		}
		return m, cmd
	}
	return m, nil
}

// filteredProjects returns the projects matching the current picker search
// text (case-insensitive substring match), or all projects when the filter
// is empty.
func (m Model) filteredProjects() []string {
	q := strings.ToLower(strings.TrimSpace(m.nsFilter.Value()))
	if q == "" {
		return m.projects
	}
	out := make([]string, 0, len(m.projects))
	for _, n := range m.projects {
		if strings.Contains(strings.ToLower(n), q) {
			out = append(out, n)
		}
	}
	return out
}

// startWithProject transitions out of the picker into the running phase
// for the given project, clearing stale data and dispatching initial
// loaders + the refresh ticker.
func (m Model) startWithProject(p string) (Model, tea.Cmd) {
	m.project = p
	m.client.SetProject(p)
	m.phase = phaseRunning
	m.nsFilter.Blur()
	// Clear stale data and force a fresh load + tick loop.
	m.deploys = nil
	m.freights = nil
	m.visibleDeploys = nil
	m.visibleFreights = nil
	m.refreshRows()
	// Re-fit tables in case window size message already arrived.
	if m.width > 0 {
		h := m.height - 4
		if h < 3 {
			h = 3
		}
		m.deploysTable.SetHeight(h)
		m.deploysTable.SetWidth(m.width)
		m.freightsTable.SetHeight(h)
		m.freightsTable.SetWidth(m.width)
	}
	m.refreshPanel()
	m.restartStageWatch()
	m.loading = true
	return m, tea.Batch(
		loadDeploysCmd(m.client, p),
		loadFreightsCmd(m.client, p),
		tickCmd(),
	)
}

// pickerView renders the project picker (used during phasePickingProject).
func (m Model) pickerView() tea.View {
	titleStyle := lipgloss.NewStyle().Foreground(normal).Bold(true).Background(bg)
	hintStyle := lipgloss.NewStyle().Foreground(muted).Background(bg)
	itemStyle := lipgloss.NewStyle().Foreground(normal).Background(bg)
	selStyle := lipgloss.NewStyle().Foreground(bg).Background(selected).Bold(true)
	errStyle := lipgloss.NewStyle().Foreground(degraded).Background(bg)

	innerW := popupInnerWidth(m.width)

	var lines []string
	lines = append(lines, titleStyle.Render("Select a Kargo project"))
	lines = append(lines, hintStyle.Render(wrap("type to filter · ↑/↓ select · enter open · r reload · esc quit", innerW)))
	lines = append(lines, "")
	filterRow := lipgloss.Height(strings.Join(lines, "\n"))
	lines = append(lines, m.nsFilter.View())
	lines = append(lines, "")

	if m.projectsError != nil {
		lines = append(lines, errStyle.Render(wrap("error: "+m.projectsError.Error(), innerW)))
	} else if m.nsLoading {
		lines = append(lines, hintStyle.Render("loading projects…"))
	} else {
		filtered := m.filteredProjects()
		if len(filtered) == 0 {
			lines = append(lines, hintStyle.Render("no Kargo projects found"))
		} else {
			maxItems := m.height - len(lines) - 4
			if maxItems < 5 {
				maxItems = 5
			}
			start := 0
			if m.nsCursor >= maxItems {
				start = m.nsCursor - maxItems + 1
			}
			end := start + maxItems
			if end > len(filtered) {
				end = len(filtered)
			}
			for i := start; i < end; i++ {
				marker := "  "
				name := wrapIndent(filtered[i], innerW-2, "  ")
				if i == m.nsCursor {
					marker = "▌ "
					lines = append(lines, selStyle.Render(marker+name))
				} else {
					lines = append(lines, itemStyle.Render(marker+name))
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

	v := tea.NewView(box)
	v.AltScreen = true
	v.BackgroundColor = bg
	if c := m.nsFilter.Cursor(); c != nil {
		// Box: border (1) + Padding(1,2). Offset the input's intrinsic
		// (x, 0) cursor by the popup's top-left content origin plus the
		// filter line's position inside the body.
		c.Position.X += 3
		c.Position.Y += 2 + filterRow
		v.Cursor = c
	}
	return v
}
