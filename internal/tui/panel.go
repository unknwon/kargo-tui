package tui

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"

	"unknwon.dev/kargo-tui/internal/kargo"
)

// resetPanelScroll moves the panel viewport back to the top. Call when the
// selection or current view changes so the panel doesn't appear stuck at a
// previous scroll position for a different item.
func (m *Model) resetPanelScroll() {
	m.panelVP.GotoTop()
}

// refreshPanel recomputes panel content and pushes it into the viewport.
// Must be called from Update so the mutation persists in the model. View()
// also re-syncs width/height/content based on the latest layout, but the
// initial content load needs to happen here so scroll commands have lines
// to work with.
func (m *Model) refreshPanel() {
	_, pw := m.layoutDims()
	if m.detailsOnly {
		pw = m.width
	}
	innerW := pw - 4
	if innerW < 8 {
		innerW = 8
	}
	lines := m.composePanelLines(innerW)
	m.panelVP.SetContent(strings.Join(lines, "\n"))
}

// composePanelLines returns the body lines for the side panel based on the
// current view and selection.
func (m Model) composePanelLines(innerW int) []string {
	titleStyle := lipgloss.NewStyle().Foreground(normal).Bold(true).Background(bg)
	keyStyle := lipgloss.NewStyle().Foreground(muted).Background(bg)
	valStyle := lipgloss.NewStyle().Foreground(normal).Background(bg)

	row := func(k, v string) string {
		return keyStyle.Render(padRight(k+":", 12)) + " " + valStyle.Render(wrap(v, innerW-13))
	}
	// rowStyled is like row but leaves the pre-colored value untouched, so
	// state cells (Health, Last Promo, …) keep their semantic color.
	rowStyled := func(k, v string) string {
		return keyStyle.Render(padRight(k+":", 12)) + " " + v
	}

	var lines []string
	switch m.view {
	case viewDeploys, viewControlFlow:
		s := m.selectedStage()
		if s == nil {
			return []string{keyStyle.Render("no selection")}
		}
		lines = append(lines, stageNameCell(s.Name, s.Health))
		if s.IsControlFlow {
			lines = append(lines, lipgloss.NewStyle().Foreground(progressing).Background(bg).Italic(true).Render("control-flow stage"))
		}
		lines = append(lines, "")
		lines = append(lines, rowStyled("Health", healthCell(s.Health)))
		if len(s.HealthIssues) > 0 {
			for i, iss := range s.HealthIssues {
				key := "Issue"
				if i > 0 {
					key = ""
				}
				lines = append(lines, row(key, iss))
			}
		}
		lines = append(lines, row("Shard", emptyDash(s.Shard)))
		freightVal := s.FreightSummary
		if isFreightName(freightVal) {
			short := freightVal[:8]
			if a := m.aliasOf(freightVal); a != "" {
				freightVal = short + " (" + a + ")"
			} else {
				freightVal = short
			}
		}
		lines = append(lines, row("Freight", emptyDash(freightVal)))
		lines = append(lines, rowStyled("Last Promo", promoCell(s.LastPromo)))
		if s.LastPromoName != "" {
			lines = append(lines, row("Promo Name", s.LastPromoName))
		}
		if !s.LastPromoAt.IsZero() {
			lines = append(lines, row("Promoted", ageString(s.LastPromoAt)+" ago"))
		}
		lines = append(lines, row("Age", ageString(s.Created)))
		if len(s.CurrentFreight) > 0 {
			lines = append(lines, "")
			lines = append(lines, keyStyle.Render("Current Freight:"))
			for _, fn := range s.CurrentFreight {
				disp := fn
				if isFreightName(disp) {
					disp = disp[:8]
					if a := m.aliasOf(fn); a != "" {
						disp += " (" + a + ")"
					}
				}
				lines = append(lines, valStyle.Render("  • "+wrap(disp, innerW-4)))
			}
		}
		if len(s.ArgoCDApps) > 0 {
			lines = append(lines, "")
			lines = append(lines, keyStyle.Render("Argo CD Apps:"))
			for _, app := range s.ArgoCDApps {
				lines = append(lines, valStyle.Render("  • "+wrap(app.Namespace+"/"+app.Name, innerW-4)))
				if app.Health != "" || app.Sync != "" {
					lines = append(lines, "    "+argoHealthCell(app.Health)+keyStyle.Render(" / ")+argoSyncCell(app.Sync))
				}
				if m.argoBaseURL != "" {
					lines = append(lines, keyStyle.Render("    "+wrap(argoAppURL(m.argoBaseURL, app), innerW-4)))
				}
			}
		}
		if len(s.Labels) > 0 {
			lines = append(lines, "")
			lines = append(lines, keyStyle.Render("Labels:"))
			for _, k := range sortedKeys(s.Labels) {
				lines = append(lines, valStyle.Render("  "+wrap(k+"="+s.Labels[k], innerW-2)))
			}
		}

		// Embed full details of every current freight (live metadata).
		for _, fn := range s.CurrentFreight {
			fr := m.freightByName(fn)
			if fr == nil {
				continue
			}
			lines = append(lines, "")
			lines = append(lines, keyStyle.Render(strings.Repeat("─", innerW)))
			lines = append(lines, titleStyle.Render("Freight "+fn[:min(8, len(fn))]))
			if fr.Alias != "" {
				lines = append(lines, keyStyle.Render(fr.Alias))
			}
			lines = append(lines, freightDetailLines(*fr, innerW, row, keyStyle, valStyle)...)
		}

	case viewFreights:
		f := m.selectedFreight()
		if f == nil {
			return []string{keyStyle.Render("no selection")}
		}
		lines = append(lines, titleStyle.Render(f.Name))
		if f.Alias != "" {
			lines = append(lines, keyStyle.Render(f.Alias))
		}
		lines = append(lines, freightDetailLines(*f, innerW, row, keyStyle, valStyle)...)
	}

	return lines
}

// freightDetailLines renders the body of a freight's detail block (without
// title), so it can be reused inside the Stage panel.
func freightDetailLines(
	f kargo.Freight,
	innerW int,
	row func(string, string) string,
	keyStyle, valStyle lipgloss.Style,
) []string {
	var lines []string
	lines = append(lines, "")
	lines = append(lines, row("Warehouse", emptyDash(f.Warehouse)))
	lines = append(lines, row("Created", f.Created.Local().Format("2006-01-02 15:04:05 MST")))
	lines = append(lines, row("Age", ageString(f.Created)))
	lines = append(lines, row("Verified", fmt.Sprintf("%d", f.VerifiedIn)))
	lines = append(lines, row("Approved", fmt.Sprintf("%d", f.ApprovedFor)))
	if len(f.CurrentlyIn) > 0 {
		lines = append(lines, "")
		lines = append(lines, keyStyle.Render("Currently In:"))
		for _, st := range f.CurrentlyIn {
			lines = append(lines, valStyle.Render("  • "+st))
		}
	}
	if len(f.VerifiedStages) > 0 {
		lines = append(lines, "")
		lines = append(lines, keyStyle.Render("Verified In:"))
		for _, st := range f.VerifiedStages {
			lines = append(lines, valStyle.Render("  • "+st))
		}
	}
	if len(f.ApprovedStages) > 0 {
		lines = append(lines, "")
		lines = append(lines, keyStyle.Render("Approved For:"))
		for _, st := range f.ApprovedStages {
			lines = append(lines, valStyle.Render("  • "+st))
		}
	}
	if len(f.Commits) > 0 {
		lines = append(lines, "")
		lines = append(lines, keyStyle.Render("Commits:"))
		for _, c := range f.Commits {
			lines = append(lines, valStyle.Render("  • "+wrap(c.RepoURL, innerW-4)))
			lines = append(lines, valStyle.Render("    "+wrap(c.ID, innerW-4)))
			if c.Branch != "" {
				lines = append(lines, keyStyle.Render("    branch: "+c.Branch))
			}
			if c.Tag != "" {
				lines = append(lines, keyStyle.Render("    tag:    "+c.Tag))
			}
			if c.Author != "" {
				lines = append(lines, keyStyle.Render("    author: "+c.Author))
			}
			if c.Message != "" {
				lines = append(lines, valStyle.Render("    "+wrap(c.Message, innerW-4)))
			}
		}
	}
	if len(f.Images) > 0 {
		lines = append(lines, "")
		lines = append(lines, keyStyle.Render("Images:"))
		for _, im := range f.Images {
			lines = append(lines, valStyle.Render("  • "+wrap(im.RepoURL, innerW-4)))
			if im.Tag != "" {
				lines = append(lines, keyStyle.Render("    tag:    "+im.Tag))
			}
			if im.Digest != "" {
				lines = append(lines, valStyle.Render("    "+wrap(im.Digest, innerW-4)))
			}
		}
	}
	if len(f.Charts) > 0 {
		lines = append(lines, "")
		lines = append(lines, keyStyle.Render("Charts:"))
		for _, ch := range f.Charts {
			name := ch.Name
			if name != "" {
				name = "/" + name
			}
			lines = append(lines, valStyle.Render("  • "+wrap(ch.RepoURL+name, innerW-4)))
			if ch.Version != "" {
				lines = append(lines, keyStyle.Render("    version: "+ch.Version))
			}
		}
	}
	return lines
}

// freightByName returns the loaded freight with the given name, or nil.
func (m Model) freightByName(name string) *kargo.Freight {
	for i := range m.freights {
		if m.freights[i].Name == name {
			return &m.freights[i]
		}
	}
	return nil
}

// renderPanel sets up the viewport for the panel and returns the bordered,
// scrollable render. Mutates m.panelVP and m.panelKey on the receiver. Called
// via a local copy in View(), so changes only reflect in the rendered frame.
func (m *Model) renderPanel(width, height int) string {
	keyStyle := lipgloss.NewStyle().Foreground(muted).Background(bg)
	border := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		Background(bg).
		Foreground(normal).
		Padding(0, 1).
		Width(width).
		Height(height)
	if m.detailsOnly {
		border = border.BorderForeground(selected)
	} else {
		border = border.BorderForeground(muted)
	}

	innerW := width - 4 // 2 for borders + 2 for padding
	if innerW < 8 {
		innerW = 8
	}
	innerH := height - 2 // 2 for borders
	if innerH < 3 {
		innerH = 3
	}

	lines := m.composePanelLines(innerW)
	body := strings.Join(lines, "\n")

	m.panelVP.SetWidth(innerW)
	m.panelVP.SetHeight(innerH)
	m.panelVP.SetContent(body)

	content := m.panelVP.View()
	if !m.panelVP.AtBottom() {
		content += "\n" + keyStyle.Render("↓ more")
	}
	return border.Render(content)
}

func (m Model) selectedStage() *kargo.Stage {
	i := m.deploysTable.Cursor()
	if i < 0 || i >= len(m.visibleDeploys) {
		return nil
	}
	s := m.visibleDeploys[i]
	return &s
}

func (m Model) selectedFreight() *kargo.Freight {
	i := m.freightsTable.Cursor()
	if i < 0 || i >= len(m.visibleFreights) {
		return nil
	}
	f := m.visibleFreights[i]
	return &f
}
