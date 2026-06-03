package tui

import (
	"context"
	"fmt"
	"time"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"

	"unknwon.dev/kargo-tui/internal/kargo"
)

// startReloginCurrentContext launches the SSO re-login flow against the
// model's current context and returns the dispatch command. Returns
// (nil, false) when re-login isn't possible: ctxRelogin wasn't wired by
// main, or there's no contextName yet. Mutates picker state to show the
// "Re-authenticating…" status, so callers don't need to set it up
// themselves. Used by both the `R` hotkey on the running view and the
// cold-start project picker when the saved token has already expired.
func (m *Model) startReloginCurrentContext() (tea.Cmd, bool) {
	if m.ctxRelogin == nil || m.contextName == "" {
		return nil, false
	}
	name := m.contextName
	relogin := m.ctxRelogin
	url := ""
	if m.client != nil {
		url = m.client.BaseURL()
	}
	m.phase = phasePickingContext
	m.ctxAdding = false
	m.ctxLoggingIn = true
	if url != "" {
		m.ctxLoginStatus = "Re-authenticating against " + url + "…"
	} else {
		m.ctxLoginStatus = "Re-authenticating " + name + "…"
	}
	m.ctxError = nil
	lctx, cancel := context.WithCancel(context.Background())
	m.ctxLoginCancel = cancel
	send := m.ctxSend
	if send == nil {
		send = func(tea.Msg) {}
	}
	// Adapt the name-based relogin callback to the URL-based shape
	// runContextLoginCmd expects. The name and relogin func are closed
	// over by value here so a later mutation of the model can't change
	// what the goroutine ends up calling.
	loginByName := func(ctx context.Context, _ string, status func(string)) (string, error) {
		return relogin(ctx, name, status)
	}
	return runContextLoginCmd(loginByName, lctx, name, send), true
}

// Update wraps the real reducer in a panic recovery shim so a bug in any
// handler surfaces as a copyable popup instead of tearing the program
// down. The deferred closure captures `m` by reference, so any mutations
// the inner reducer made before the panic are still visible here and get
// returned. The model can be partially-updated but the popup always
// shows, which is the property we care about.
func (m Model) Update(msg tea.Msg) (out tea.Model, cmd tea.Cmd) {
	defer func() {
		if r := recover(); r != nil {
			m.panicMessage = formatPanic(r)
			m.preparePanicViewport()
			out, cmd = m, nil
		}
	}()
	out, cmd = m.updateInner(msg)
	// Refresh the graph pan offset after every update so the cursor stays
	// visible without snapping the viewport when the cursor is already in
	// view. Cheap and uniform: avoids sprinkling recomputeGraphPan calls
	// across every cursor / resize / data-load handler.
	if mm, ok := out.(Model); ok {
		mm.recomputeGraphPan()
		mm.recomputeListScrolls()
		out = mm
	}
	return out, cmd
}

func (m Model) updateInner(msg tea.Msg) (tea.Model, tea.Cmd) {
	if sm, ok := msg.(SetSendMsg); ok {
		m.ctxSend = sm.Send
		m.restartStageWatch()
		return m, nil
	}

	// Panic overlay takes precedence over every other key handler so a
	// recovered-but-still-buggy state can always be dismissed. esc clears
	// the popup; everything else routes to the trace viewport so the user
	// can scroll a long stack. Non-key messages (ticks, watch updates)
	// are dropped while the popup is up — the underlying state may be
	// inconsistent and we don't want to re-trigger the same panic.
	if m.panicMessage != "" {
		switch msg := msg.(type) {
		case tea.WindowSizeMsg:
			m.width, m.height = msg.Width, msg.Height
			m.preparePanicViewport()
			return m, nil
		case tea.KeyPressMsg:
			switch msg.String() {
			case "esc":
				m.panicMessage = ""
				return m, nil
			case "q", "ctrl+c":
				// Always offer an exit path: if the underlying state
				// keeps re-panicking after dismiss, esc alone won't
				// help. Mirror the help/overlay/picker convention so
				// the user is never stuck.
				return m, tea.Quit
			}
			var cmd tea.Cmd
			m.panicVP, cmd = m.panicVP.Update(msg)
			return m, cmd
		case tea.MouseWheelMsg:
			// Mirror the help/logs/diff overlays so the trace is
			// wheel-scrollable too — a deep stack is hard to read
			// without it.
			switch msg.Button {
			case tea.MouseWheelUp:
				m.panicVP.ScrollUp(3)
			case tea.MouseWheelDown:
				m.panicVP.ScrollDown(3)
			}
			return m, nil
		}
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
			// Mirror the per-keystroke filter dispatch below: graph
			// view drives a search instead of filtering rows, so a
			// paste during graph filtering needs recomputeGraphMatches
			// to keep matches/cursor consistent with the new query.
			if m.view == viewGraph {
				m.recomputeGraphMatches(m.filter.Value())
			} else {
				m.refreshRows()
			}
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
		// Reload help content so its viewport's max-Y-offset reflects
		// the new size — without this, scroll keys are no-ops after
		// resize until the help is re-opened.
		if m.showHelp {
			m.prepareHelpViewport()
		}
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
			m.noteAuthFailure(msg.err)
		} else {
			m.deploysError = nil
			m.deploys = msg.deploys
			m.noteAuthSuccess()
			m.refreshRows()
			m.refreshPanel()
		}
		return m, nil

	case freightsLoadedMsg:
		m.loading = false
		if msg.err != nil {
			m.freightsError = msg.err
			m.noteAuthFailure(msg.err)
		} else {
			m.freightsError = nil
			m.freights = msg.freights
			m.noteAuthSuccess()
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
		if msg.err != nil {
			m.noteAuthFailure(msg.err)
		} else {
			m.noteAuthSuccess()
		}
		m.renderLogs()
		return m, nil

	case argoShardsMsg:
		m.argoShards = kargo.ArgoCDShards(msg)
		m.refreshPanel()
		return m, nil

	case stageEventMsg:
		m.deploys = kargo.MergeStageEvent(m.deploys, kargo.StageEvent(msg))
		m.refreshRows()
		m.refreshPanel()
		return m, nil

	case stageWatchEndedMsg:
		// Stream closed (server hangup, network blip, or proxy stripped
		// the streaming response). Tick-based refresh is still running,
		// so the UI keeps working — surface a quiet status note.
		if msg.err != nil {
			if kargo.IsUnauthenticated(msg.err) {
				m.noteAuthFailure(msg.err)
			} else {
				m.yankedMessage = "stage watch ended: " + msg.err.Error()
				m.yankedAt = time.Now()
			}
		}
		m.stageWatchCancel = nil
		return m, nil

	case promoteDownstreamResultMsg:
		if msg.err != nil {
			m.noteAuthFailure(msg.err)
			m.promoteError = msg.err
			m.yankedMessage = "promote-downstream failed: " + msg.err.Error()
		} else if msg.promotions == 0 {
			m.promoteResult = fmt.Sprintf("no eligible downstream stages for %s", msg.source)
			m.yankedMessage = "no eligible downstream stages for " + msg.source
		} else {
			m.promoteResult = fmt.Sprintf("%d downstream promotion(s) from %s", msg.promotions, msg.source)
			m.yankedMessage = fmt.Sprintf("created %d downstream promotion(s) from %s", msg.promotions, msg.source)
		}
		m.yankedAt = time.Now()
		// Same as promoteResultMsg: if the overlay is still up, advance
		// it to the done step so the user sees the result and can
		// dismiss. Without this transition the overlay would hang on
		// "submitting promotion…".
		if m.overlay == overlayPromote {
			m.promoteStep = promoteDone
		}
		// Force an immediate data refresh so the new promotion appears
		// without waiting for the next tick. We dispatch even when a
		// tick fetch is already in flight: the in-flight one was issued
		// before the promotion existed, so its data won't include it.
		m.loading = true
		return m, tea.Batch(
			loadDeploysCmd(m.client, m.project),
			loadFreightsCmd(m.client, m.project),
		)

	case promoteResultMsg:
		// The overlay may have been dismissed before the response landed
		// (the user hit esc on the submitting screen). We still record
		// the outcome as a transient yank-style status so they see it.
		if msg.err != nil {
			m.noteAuthFailure(msg.err)
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
		// the deploy/tree views without waiting for the next tick. We
		// dispatch even when a tick fetch is in flight — that one was
		// issued before the promotion existed, so its data won't have
		// it.
		m.loading = true
		return m, tea.Batch(
			loadDeploysCmd(m.client, m.project),
			loadFreightsCmd(m.client, m.project),
		)

	case tea.MouseWheelMsg:
		// Mouse wheel scrolls whichever surface is currently visible:
		// help and the logs/diff overlay both have their own viewport, the
		// details panel has its viewport when it's full-screen, and
		// otherwise we move the table cursor a row at a time.
		//
		// Shift+wheel translates a vertical wheel into a horizontal
		// column scroll on the list views. Some terminals also emit
		// dedicated MouseWheelLeft/Right events (e.g. tilted wheels and
		// trackpads), so those are handled directly.
		if m.menuOpen {
			return m, nil
		}
		shift := msg.Mod&tea.ModShift != 0
		switch msg.Button {
		case tea.MouseWheelUp:
			switch {
			case m.showHelp:
				m.helpVP.ScrollUp(3)
			case m.overlay != overlayNone:
				m.overlayVP.ScrollUp(3)
			case m.detailsOnly:
				m.panelVP.ScrollUp(3)
			case shift && m.activeTable() != nil:
				m.scrollLeft()
			case shift && m.view == viewGraph:
				if m.moveGraphCursor("left") {
					m.resetPanelScroll()
					m.refreshPanel()
				}
			case m.view == viewGraph:
				if m.moveGraphCursor("up") {
					m.resetPanelScroll()
					m.refreshPanel()
				}
			case m.view == viewTree:
				m.moveTreeCursor(-1)
				m.resetPanelScroll()
				m.refreshPanel()
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
			case shift && m.activeTable() != nil:
				m.scrollRight()
			case shift && m.view == viewGraph:
				if m.moveGraphCursor("right") {
					m.resetPanelScroll()
					m.refreshPanel()
				}
			case m.view == viewGraph:
				if m.moveGraphCursor("down") {
					m.resetPanelScroll()
					m.refreshPanel()
				}
			case m.view == viewTree:
				m.moveTreeCursor(1)
				m.resetPanelScroll()
				m.refreshPanel()
			default:
				m.moveCursor(1)
			}
		case tea.MouseWheelLeft:
			switch {
			case m.activeTable() != nil:
				m.scrollLeft()
			case m.view == viewGraph:
				if m.moveGraphCursor("left") {
					m.resetPanelScroll()
					m.refreshPanel()
				}
			}
		case tea.MouseWheelRight:
			switch {
			case m.activeTable() != nil:
				m.scrollRight()
			case m.view == viewGraph:
				if m.moveGraphCursor("right") {
					m.resetPanelScroll()
					m.refreshPanel()
				}
			}
		}
		return m, nil

	case tea.MouseClickMsg:
		return m.handleMouseClick(msg)

	case tea.MouseMotionMsg:
		if m.menuOpen {
			if idx := m.menuHitTest(msg.X, msg.Y); idx >= 0 {
				m.menuCursor = idx
			}
		}
		return m, nil

	case tea.KeyPressMsg:
		key := msg.String()

		// Right-click context menu owns input while it's open: arrows
		// move the highlight, enter picks, esc/q dismisses. Letting
		// anything else through would race with the underlying view's
		// shortcuts.
		if m.menuOpen {
			switch key {
			case "esc", "q":
				m.closeMenu()
				return m, nil
			case "ctrl+c":
				return m, tea.Quit
			case "up", "k":
				if m.menuCursor > 0 {
					m.menuCursor--
				}
				return m, nil
			case "down", "j":
				if m.menuCursor < len(m.menuItems)-1 {
					m.menuCursor++
				}
				return m, nil
			case "enter":
				newM, cmd := m.invokeMenuItem(m.menuCursor)
				return newM, cmd
			}
			return m, nil
		}

		if m.filtering {
			switch key {
			case "esc":
				m.filtering = false
				m.filter.Blur()
				m.filter.SetValue("")
				if m.view == viewGraph {
					// Graph search: drop the matches *and* roll the
					// cursor back to where the user started. esc on
					// list views just clears the row filter; graph has
					// to undo the spatial jump too.
					m.cancelGraphSearch()
				} else {
					m.refreshRows()
				}
				return m, nil
			case "enter":
				m.filtering = false
				m.filter.Blur()
				if m.view == viewGraph {
					// Commit the search: keep the matches around so n/N
					// can step through them, but leave the active flag
					// false — the cursor stays on the current match.
					m.graphSearchActive = false
				}
				return m, nil
			}
			var cmd tea.Cmd
			m.filter, cmd = m.filter.Update(msg)
			if m.view == viewGraph {
				// Live-search: every keystroke recomputes matches and
				// jumps the cursor to the first hit. The list/tree
				// views filter rows instead via refreshRows.
				m.recomputeGraphMatches(m.filter.Value())
			} else {
				m.refreshRows()
			}
			return m, cmd
		}

		// Help overlay: switch tabs, scroll, and dismiss only.
		if m.showHelp {
			switch key {
			case "q", "ctrl+c":
				return m, tea.Quit
			case "esc", "?", "enter":
				m.showHelp = false
				return m, nil
			case "tab", "]", "right":
				m.switchHelpTab(1)
				return m, nil
			case "shift+tab", "[", "left":
				m.switchHelpTab(-1)
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
			case "home":
				m.helpVP.GotoTop()
				return m, nil
			case "end":
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
			case "tab", "]", "shift+tab", "[":
				if m.overlay == overlayLogs {
					if key == "shift+tab" || key == "[" {
						if m.overlayLogsTab == logsTabPromotions {
							m.overlayLogsTab = logsTabEvents
						} else {
							m.overlayLogsTab = logsTabPromotions
						}
					} else {
						if m.overlayLogsTab == logsTabEvents {
							m.overlayLogsTab = logsTabPromotions
						} else {
							m.overlayLogsTab = logsTabEvents
						}
					}
					// Skip render while the fetch is in flight: overlayPromos
					// and overlayEvents are still nil, so renderLogs would
					// replace the "loading…" placeholder with "(none)".
					if !m.overlayLoading {
						m.overlayVP.GotoTop()
						m.renderLogs()
					}
				}
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
			case "home":
				m.overlayVP.GotoTop()
				return m, nil
			case "end":
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
			if m.view == viewGraph {
				m.beginGraphSearch()
			}
			cmd := m.filter.Focus()
			return m, cmd
		case "esc":
			// Dismiss details overlay on the first press; clear filter on a
			// subsequent press. In graph view a committed search also
			// leaves a match list behind that n/N steps through; clear
			// that too so esc fully exits the search state instead of
			// leaving stale matches that the persistent line hides but
			// n/N still cycles through.
			if m.detailsOnly {
				m.detailsOnly = false
				return m, nil
			}
			if m.filter.Value() != "" {
				m.filter.SetValue("")
				if m.view == viewGraph {
					m.graphSearchMatches = nil
					m.graphSearchPos = 0
					m.graphSearchActive = false
				} else {
					m.refreshRows()
				}
			} else if m.view == viewGraph && len(m.graphSearchMatches) > 0 {
				// Defensive: filter was cleared by some other path but
				// the match list lingered. esc should still tidy it up.
				m.graphSearchMatches = nil
				m.graphSearchPos = 0
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
			// In full-screen details mode, arrows scroll the panel
			// regardless of the structural view behind it. Otherwise
			// graph and tree views consume arrows themselves (spatial
			// cursor / collapse) and tables get column scroll.
			if m.detailsOnly {
				m.panelVP.ScrollLeft(2)
				return m, nil
			}
			if m.view == viewGraph {
				m.moveGraphCursor("left")
				return m, nil
			}
			if m.view == viewTree {
				m.setTreeNodeExpansion(false)
				return m, nil
			}
			m.scrollLeft()
			return m, nil
		case "right":
			if m.detailsOnly {
				m.panelVP.ScrollRight(2)
				return m, nil
			}
			if m.view == viewGraph {
				m.moveGraphCursor("right")
				return m, nil
			}
			if m.view == viewTree {
				m.setTreeNodeExpansion(true)
				return m, nil
			}
			m.scrollRight()
			return m, nil
		case "up", "k":
			if m.detailsOnly {
				m.panelVP.ScrollUp(1)
				return m, nil
			}
			if m.view == viewGraph {
				m.moveGraphCursor("up")
				return m, nil
			}
			if m.view == viewTree {
				m.moveTreeCursor(-1)
				return m, nil
			}
		case "down", "j":
			if m.detailsOnly {
				m.panelVP.ScrollDown(1)
				return m, nil
			}
			if m.view == viewGraph {
				m.moveGraphCursor("down")
				return m, nil
			}
			if m.view == viewTree {
				m.moveTreeCursor(1)
				return m, nil
			}
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
		case "R":
			// Inline re-login for the current context. Only meaningful when
			// the auth banner is up; otherwise the existing session is fine
			// and `R` does nothing (avoids surprising the user).
			if !m.authExpired {
				return m, nil
			}
			cmd, ok := m.startReloginCurrentContext()
			if !ok {
				return m, nil
			}
			return m, cmd
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
			m.setView(viewGraph)
			return m, nil
		case "P":
			s := m.selectedStage()
			if s == nil {
				return m, nil
			}
			m.openPromoteOverlay(s)
			return m, nil
		case ">":
			// Open the downstream-promote overlay. The picker always
			// shows the full freight list (newest-first, eligible
			// first), with the currently-deployed freight marked, so
			// every stage shape uses the same flow. The overlay
			// confirms (y/n) before firing the RPC.
			s := m.selectedStage()
			if s == nil {
				return m, nil
			}
			m.openPromoteDownstreamOverlay(s)
			return m, nil
		case "n":
			// Graph view only: step to the next saved search match.
			// No-op when there are no matches (the search was either
			// never started or returned nothing).
			if m.view == viewGraph && len(m.graphSearchMatches) > 0 {
				m.stepGraphMatch(1)
				return m, nil
			}
		case "N":
			if m.view == viewGraph && len(m.graphSearchMatches) > 0 {
				m.stepGraphMatch(-1)
				return m, nil
			}
		case "?":
			m.showHelp = true
			m.helpTab = helpTabKeybindings
			m.prepareHelpViewport()
			return m, nil
		case "M":
			m.mouseEnabled = !m.mouseEnabled
			if m.mouseEnabled {
				m.yankedMessage = "mouse capture on"
			} else {
				m.yankedMessage = "mouse capture off (terminal selection enabled)"
			}
			m.yankedAt = time.Now()
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

		// Graph view consumes any other unhandled key so it doesn't leak
		// into the hidden table dispatch below. Arrow keys are handled
		// in the top-level switch; logs / promote / diff use l / P / D
		// like every other view. Exception: in detailsOnly mode the
		// panel viewport owns scroll keys (pgup/pgdn/home/end) so let
		// them fall through to the panel scroll handler below.
		if m.view == viewGraph && !m.detailsOnly {
			var moved bool
			switch key {
			case "pgup":
				moved = m.moveGraphCursorWithin(-10)
			case "pgdown", "pgdn", " ":
				moved = m.moveGraphCursorWithin(10)
			case "home":
				moved = m.moveGraphCursorWithin(-len(m.graphLayout.nodes))
			case "end":
				moved = m.moveGraphCursorWithin(len(m.graphLayout.nodes))
			default:
				return m, nil
			}
			if moved {
				m.resetPanelScroll()
				m.refreshPanel()
			}
			return m, nil
		}

		// Tree view owns its own page/expand/toggle keys. Arrow + j/k
		// nav was already handled in the top-level switch above. Same
		// exception as graph: in detailsOnly mode let scroll keys fall
		// through to the panel handler.
		if m.view == viewTree && !m.detailsOnly {
			switch key {
			case "pgup":
				m.moveTreeCursor(-10)
				return m, nil
			case "pgdown", "pgdn", " ":
				m.moveTreeCursor(10)
				return m, nil
			case "home":
				m.treeCursor = 0
				return m, nil
			case "end":
				if len(m.treeNodes) > 0 {
					m.treeCursor = len(m.treeNodes) - 1
				}
				return m, nil
			case "+":
				m.setTreeNodeExpansion(true)
				return m, nil
			case "-":
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
			case "home":
				m.panelVP.GotoTop()
				return m, nil
			case "end":
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

// recomputeListScrolls refreshes the sticky scroll offsets for every
// custom list view (tree, project picker, context picker, freight
// picker) so the cursor row stays visible without snapping the viewport
// when the cursor is already in view. Called once at the tail of Update,
// mirroring the recomputeGraphPan pattern so the scroll math doesn't
// have to be sprinkled across every cursor / resize / data-load handler.
func (m *Model) recomputeListScrolls() {
	switch {
	case m.phase == phasePickingProject:
		m.nsScroll = clampListScroll(m.nsScroll, m.nsCursor, m.nsBodyHeight(), len(m.filteredProjects()))
	case m.phase == phasePickingContext:
		m.ctxScroll = clampListScroll(m.ctxScroll, m.ctxCursor, m.ctxBodyHeight(), len(m.filteredContexts()))
	case m.overlay == overlayPromote:
		m.promoteScroll = clampListScroll(m.promoteScroll, m.promoteCursor, m.promoteBodyHeight(), len(m.promoteCandidates))
	case m.view == viewTree:
		m.treeScroll = clampListScroll(m.treeScroll, m.treeCursor, m.treeBodyHeight(), len(m.treeNodes))
	}
}
