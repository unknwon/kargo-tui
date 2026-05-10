package tui

import (
	"time"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
)

// layoutDims returns (tableWidth, panelWidth). Panel is shown only when
// terminal is wide enough; otherwise it gets 0 width and is hidden.
func (m Model) layoutDims() (int, int) {
	const minPanel = 32
	const gap = 1
	if m.width < 80 {
		return m.width, 0
	}
	panel := m.width / 3
	if panel < minPanel {
		panel = minPanel
	}
	if panel > 60 {
		panel = 60
	}
	tw := m.width - panel - gap
	if tw < 40 {
		tw = m.width
		panel = 0
	}
	return tw, panel
}

// Update is the Bubble Tea reducer. It routes window resizes, refresh ticks,
// loaded data, key presses, and overlay/picker events to the right handler
// and returns a possibly-mutated model plus any follow-up commands.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if sm, ok := msg.(SetSendMsg); ok {
		m.ctxSend = sm.Send
		return m, nil
	}

	if m.phase == phasePickingProject {
		return m.updatePicker(msg)
	}
	if m.phase == phasePickingContext {
		return m.updateContextPicker(msg)
	}

	// Forward terminal paste events to the filter text input when the user
	// is actively filtering. Without this the textinput never sees the
	// pasted content because the main key switch only handles KeyPressMsg.
	if pm, ok := msg.(tea.PasteMsg); ok {
		if m.filtering {
			var cmd tea.Cmd
			m.filter, cmd = m.filter.Update(pm)
			m.refreshRows()
			return m, cmd
		}
		return m, nil
	}

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		h := m.height - 4
		if h < 3 {
			h = 3
		}
		m.deploysTable.SetHeight(h)
		m.deploysTable.SetWidth(m.width)
		m.freightsTable.SetHeight(h)
		m.freightsTable.SetWidth(m.width)
		// Resize the full-screen viewports too so SoftWrap recalculates and
		// scroll bounds stay correct.
		ow := m.width - 4
		oh := m.height - 8
		if ow < 20 {
			ow = 20
		}
		if oh < 5 {
			oh = 5
		}
		m.overlayVP.SetWidth(ow)
		m.overlayVP.SetHeight(oh)
		m.helpVP.SetWidth(ow)
		m.helpVP.SetHeight(oh)
		m.refreshPanel()
		return m, nil

	case tickMsg:
		if m.loading {
			return m, tickCmd()
		}
		m.loading = true
		cmds := []tea.Cmd{
			loadDeploysCmd(m.client, m.project),
			loadFreightsCmd(m.client, m.project),
			tickCmd(),
		}
		// Refresh the logs overlay in place if it's open. The logsLoadedMsg
		// handler ignores stale results when the overlay has been dismissed,
		// so a tick that races with a close is harmless.
		if m.overlay == overlayLogs && m.overlayStageName != "" {
			cmds = append(cmds, loadLogsCmd(m.client, m.project, m.overlayStageName))
		}
		return m, tea.Batch(cmds...)

	case deploysLoadedMsg:
		m.loading = false
		if msg.err != nil {
			m.deploysError = msg.err
		} else {
			m.deploysError = nil
			m.deploys = msg.deploys
			m.refreshRows()
			m.refreshPanel()
		}
		return m, nil

	case freightsLoadedMsg:
		m.loading = false
		if msg.err != nil {
			m.freightsError = msg.err
		} else {
			m.freightsError = nil
			m.freights = msg.freights
			m.refreshRows()
			m.refreshPanel()
		}
		return m, nil

	case logsLoadedMsg:
		if m.overlay != overlayLogs {
			return m, nil
		}
		m.overlayLoading = false
		m.overlayPromos = msg.promos
		m.overlayEvents = msg.events
		m.overlayError = msg.err
		m.renderLogs()
		return m, nil

	case argoURLMsg:
		m.argoBaseURL = string(msg)
		m.refreshPanel()
		return m, nil

	case promoteResultMsg:
		// The overlay may have been dismissed before the response landed
		// (the user hit esc on the submitting screen). We still record
		// the outcome as a transient yank-style status so they see it.
		if msg.err != nil {
			m.promoteError = msg.err
			m.yankedMessage = "promote failed: " + msg.err.Error()
		} else {
			m.promoteResult = msg.promotionName
			m.yankedMessage = "promotion created: " + msg.promotionName
		}
		m.yankedAt = time.Now()
		if m.overlay == overlayPromote {
			m.promoteStep = promoteDone
		}
		// Force an immediate data refresh so the new promotion appears in
		// the deploy/tree views without waiting for the next tick.
		if !m.loading {
			m.loading = true
			return m, tea.Batch(
				loadDeploysCmd(m.client, m.project),
				loadFreightsCmd(m.client, m.project),
			)
		}
		return m, nil

	case tea.MouseWheelMsg:
		// Mouse wheel scrolls whichever surface is currently visible:
		// help and the logs/diff overlay both have their own viewport, the
		// details panel has its viewport when it's full-screen, and
		// otherwise we move the table cursor a row at a time.
		switch msg.Button {
		case tea.MouseWheelUp:
			switch {
			case m.showHelp:
				m.helpVP.ScrollUp(3)
			case m.overlay != overlayNone:
				m.overlayVP.ScrollUp(3)
			case m.detailsOnly:
				m.panelVP.ScrollUp(3)
			default:
				m.moveCursor(-1)
			}
		case tea.MouseWheelDown:
			switch {
			case m.showHelp:
				m.helpVP.ScrollDown(3)
			case m.overlay != overlayNone:
				m.overlayVP.ScrollDown(3)
			case m.detailsOnly:
				m.panelVP.ScrollDown(3)
			default:
				m.moveCursor(1)
			}
		}
		return m, nil

	case tea.KeyPressMsg:
		key := msg.String()

		if m.filtering {
			switch key {
			case "esc":
				m.filtering = false
				m.filter.Blur()
				m.filter.SetValue("")
				m.refreshRows()
				return m, nil
			case "enter":
				m.filtering = false
				m.filter.Blur()
				return m, nil
			}
			var cmd tea.Cmd
			m.filter, cmd = m.filter.Update(msg)
			m.refreshRows()
			return m, cmd
		}

		// Help overlay: scroll & dismiss only.
		if m.showHelp {
			switch key {
			case "q", "ctrl+c":
				return m, tea.Quit
			case "esc", "?", "enter":
				m.showHelp = false
				return m, nil
			case "up", "k":
				m.helpVP.ScrollUp(1)
				return m, nil
			case "down", "j":
				m.helpVP.ScrollDown(1)
				return m, nil
			case "pgup":
				m.helpVP.PageUp()
				return m, nil
			case "pgdown", "pgdn", " ":
				m.helpVP.PageDown()
				return m, nil
			case "home", "g":
				m.helpVP.GotoTop()
				return m, nil
			case "end", "G":
				m.helpVP.GotoBottom()
				return m, nil
			}
			return m, nil
		}

		// Promote overlay: distinct flow (pick → confirm → submit → done).
		if m.overlay == overlayPromote {
			return m.updatePromoteOverlay(key)
		}

		// Logs/Diff overlay: scroll & dismiss only.
		if m.overlay != overlayNone {
			switch key {
			case "q", "ctrl+c":
				return m, tea.Quit
			case "esc", "enter":
				m.overlay = overlayNone
				m.overlayStageName = ""
				return m, nil
			case "up", "k":
				m.overlayVP.ScrollUp(1)
				return m, nil
			case "down", "j":
				m.overlayVP.ScrollDown(1)
				return m, nil
			case "pgup":
				m.overlayVP.PageUp()
				return m, nil
			case "pgdown", "pgdn", " ":
				m.overlayVP.PageDown()
				return m, nil
			case "home", "g":
				m.overlayVP.GotoTop()
				return m, nil
			case "end", "G":
				m.overlayVP.GotoBottom()
				return m, nil
			}
			return m, nil
		}

		switch key {
		case "q", "ctrl+c":
			return m, tea.Quit
		case "/":
			m.filtering = true
			cmd := m.filter.Focus()
			return m, cmd
		case "esc":
			// Dismiss details overlay on the first press; clear filter on a
			// subsequent press.
			if m.detailsOnly {
				m.detailsOnly = false
				return m, nil
			}
			if m.filter.Value() != "" {
				m.filter.SetValue("")
				m.refreshRows()
			}
			return m, nil
		case "d":
			m.setView(viewDeploys)
			return m, nil
		case "f":
			m.setView(viewFreights)
			return m, nil
		case "r":
			if !m.loading {
				m.loading = true
				return m, tea.Batch(
					loadDeploysCmd(m.client, m.project),
					loadFreightsCmd(m.client, m.project),
				)
			}
			return m, nil
		case "left":
			m.scrollLeft()
			return m, nil
		case "right":
			m.scrollRight()
			return m, nil
		case "p":
			// Re-open the project picker. nsExplicit prevents the
			// auto-select branch from short-circuiting the picker when only
			// one project is configured.
			m.phase = phasePickingProject
			m.nsLoading = true
			m.nsCursor = 0
			m.nsExplicit = true
			m.nsFilter.SetValue("")
			m.nsFilter.Focus()
			return m, tea.Batch(loadProjectsCmd(m.client), textinput.Blink)
		case "C":
			// Open the context picker if main wired one in via WithContexts.
			if m.ctxBuilder == nil {
				return m, nil
			}
			m.phase = phasePickingContext
			m.ctxCursor = 0
			m.ctxError = nil
			m.ctxAdding = false
			m.ctxFilter.SetValue("")
			m.ctxFilter.Focus()
			return m, textinput.Blink
		case "v":
			m.detailsOnly = !m.detailsOnly
			return m, nil
		case "c":
			m.setView(viewControlFlow)
			return m, nil
		case "t":
			m.setView(viewTree)
			return m, nil
		case "g":
			// Top-level `g` opens the graph view. In detailsOnly the
			// `g` key still goes to the panel-top scroll handler below.
			if !m.detailsOnly {
				m.setView(viewGraph)
				return m, nil
			}
		case "P":
			s := m.selectedStage()
			if s == nil {
				return m, nil
			}
			m.openPromoteOverlay(s)
			return m, nil
		case "?":
			m.showHelp = true
			return m, nil
		case "s":
			m.cycleSort()
			m.refreshRows()
			return m, nil
		case "y":
			m.yankSelection()
			return m, nil
		case "o":
			m.openArgoCDForSelection()
			return m, nil
		case "l":
			if m.view == viewDeploys || m.view == viewControlFlow || m.view == viewTree || m.view == viewGraph {
				if s := m.selectedStage(); s != nil {
					m.openLogsOverlay(s.Name)
					return m, loadLogsCmd(m.client, m.project, s.Name)
				}
			}
			return m, nil
		case "D":
			if m.view == viewDeploys || m.view == viewControlFlow || m.view == viewTree || m.view == viewGraph {
				m.openDiffOverlay()
				return m, nil
			}
			return m, nil
		}

		// Graph view: arrow keys move the cursor along edges/siblings,
		// enter opens the logs overlay for the selected node.
		if m.view == viewGraph {
			switch key {
			case "left":
				m.moveGraphCursor("left")
				return m, nil
			case "right":
				m.moveGraphCursor("right")
				return m, nil
			case "up", "k":
				m.moveGraphCursor("up")
				return m, nil
			case "down", "j":
				m.moveGraphCursor("down")
				return m, nil
			case "enter":
				if s := m.selectedStage(); s != nil {
					m.openLogsOverlay(s.Name)
					return m, loadLogsCmd(m.client, m.project, s.Name)
				}
				return m, nil
			}
			return m, nil
		}

		// Tree view owns its own navigation: arrow keys move the tree
		// cursor, +/- expand/collapse, enter toggles. Bypass the table
		// dispatch below so nothing leaks through to the hidden table.
		if m.view == viewTree {
			switch key {
			case "up", "k":
				m.moveTreeCursor(-1)
				return m, nil
			case "down", "j":
				m.moveTreeCursor(1)
				return m, nil
			case "pgup":
				m.moveTreeCursor(-10)
				return m, nil
			case "pgdown", "pgdn", " ":
				m.moveTreeCursor(10)
				return m, nil
			case "home", "g":
				m.treeCursor = 0
				return m, nil
			case "end", "G":
				if len(m.treeNodes) > 0 {
					m.treeCursor = len(m.treeNodes) - 1
				}
				return m, nil
			case "+", "right":
				m.setTreeNodeExpansion(true)
				return m, nil
			case "-", "left":
				m.setTreeNodeExpansion(false)
				return m, nil
			case "enter":
				m.toggleTreeNode()
				return m, nil
			}
			return m, nil
		}

		// In full-screen details mode the table is hidden, so nav keys scroll
		// the panel viewport instead of moving the table cursor.
		if m.detailsOnly {
			switch key {
			case "up", "k":
				m.panelVP.ScrollUp(1)
				return m, nil
			case "down", "j":
				m.panelVP.ScrollDown(1)
				return m, nil
			case "pgup":
				m.panelVP.PageUp()
				return m, nil
			case "pgdown", "pgdn", " ":
				m.panelVP.PageDown()
				return m, nil
			case "home", "g":
				m.panelVP.GotoTop()
				return m, nil
			case "end", "G":
				m.panelVP.GotoBottom()
				return m, nil
			}
		}
	}

	var cmd tea.Cmd
	switch m.view {
	case viewDeploys, viewControlFlow:
		prev := m.deploysTable.Cursor()
		m.deploysTable, cmd = m.deploysTable.Update(msg)
		applyCursorMarker(&m.deploysTable)
		if m.deploysTable.Cursor() != prev {
			m.resetPanelScroll()
			m.refreshPanel()
		}
	case viewFreights:
		prev := m.freightsTable.Cursor()
		m.freightsTable, cmd = m.freightsTable.Update(msg)
		applyCursorMarker(&m.freightsTable)
		if m.freightsTable.Cursor() != prev {
			m.resetPanelScroll()
			m.refreshPanel()
		}
	}
	return m, cmd
}
