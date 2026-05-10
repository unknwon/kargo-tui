package tui

import (
	"time"

	"charm.land/bubbles/v2/table"
	"charm.land/bubbles/v2/textinput"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"unknwon.dev/kargo-tui/internal/kargo"
)

var (
	bg          = lipgloss.Color("#070707")
	selected    = lipgloss.Color("#009fff")
	healthy     = lipgloss.Color("#00cab1")
	degraded    = lipgloss.Color("#ff2e3f")
	progressing = lipgloss.Color("#ffca00")
	muted       = lipgloss.Color("#84848a")
	normal      = lipgloss.Color("#fbfbfb")
)

type view int

const (
	viewDeploys view = iota
	viewFreights
	viewControlFlow
)

type phase int

const (
	phaseRunning phase = iota
	phasePickingProject
)

type overlayMode int

const (
	overlayNone overlayMode = iota
	overlayLogs
	overlayDiff
)

// Model is the Bubble Tea model that drives the kargo-tui interface. It
// holds every piece of state the UI needs: loaded Kargo data, table widgets,
// per-view filter/sort state, the project picker, and any active overlay.
type Model struct {
	client      *kargo.Client
	contextName string

	project string
	phase   phase
	view    view

	// detailsOnly hides the table and fills the whole screen with the side
	// panel. Useful on narrow terminals (e.g. phones) where the panel would
	// otherwise be hidden.
	detailsOnly bool

	// showHelp renders a full-screen help overlay listing key bindings.
	showHelp bool

	// panelVP holds the rendered panel content as a scrollable viewport.
	// Scroll keys are routed here only when detailsOnly is true; in the
	// side-by-side layout the panel is read-only.
	panelVP viewport.Model

	deploys       []kargo.Stage
	freights      []kargo.Freight
	deploysError  error
	freightsError error

	deploysTable  table.Model
	freightsTable table.Model

	// Visible* mirrors the items currently rendered in each table, post-filter,
	// indexed in the same order as the rows. Used to look up the selected item
	// for the metadata panel.
	visibleDeploys  []kargo.Stage
	visibleFreights []kargo.Freight

	// Horizontal scroll offset (in column count) per table view.
	deploysColOffset  int
	freightsColOffset int

	// filter is the live textinput; filterValues persists each view's filter
	// string so switching views restores the per-list query.
	filter        textinput.Model
	filtering     bool
	filterValues  map[view]string
	sort          map[view]sortMode
	argoBaseURL   string
	yankedMessage string
	yankedAt      time.Time

	// helpVP renders the keybindings overlay body so it can scroll
	// independently of the table/details viewports.
	helpVP viewport.Model

	// Logs/Diff overlay state.
	overlay         overlayMode
	overlayVP       viewport.Model
	overlayTitle    string
	overlayLoading  bool
	overlayError    error
	overlayPromos   []kargo.PromotionEntry
	overlayEvents   []kargo.EventEntry
	overlayDiffFrom *kargo.Freight
	overlayDiffTo   *kargo.Freight

	// Project picker state.
	projects      []string
	projectsError error
	nsFilter      textinput.Model
	nsCursor      int
	nsLoading     bool

	width, height int

	lastUpdate time.Time
	loading    bool
}

// allStageColumns / allFreightColumns are the full set of columns. Horizontal
// scrolling reveals additional columns by advancing colOffset; the marker
// column at index 0 is always pinned.
var allStageColumns = []table.Column{
	{Title: " ", Width: 2},
	{Title: "Name", Width: 30},
	{Title: "Health", Width: 14},
	{Title: "Freight", Width: 32},
	{Title: "Last Promo", Width: 12},
	{Title: "Shard", Width: 10},
	{Title: "Age", Width: 8},
}

var allFreightColumns = []table.Column{
	{Title: " ", Width: 2},
	{Title: "Name", Width: 30},
	{Title: "Age", Width: 8},
	{Title: "VerifiedIn", Width: 12},
	{Title: "ApprovedFor", Width: 12},
	{Title: "Warehouse", Width: 20},
}

// New starts the TUI with a known project and pre-loaded data.
func New(client *kargo.Client, contextName, project string, deploys []kargo.Stage, freights []kargo.Freight) Model {
	m := newBase()
	m.client = client
	m.contextName = contextName
	m.project = project
	m.phase = phaseRunning
	m.deploys = deploys
	m.freights = freights
	m.lastUpdate = time.Now()
	m.refreshRows()
	m.refreshPanel()
	return m
}

// NewWithPicker starts the TUI in project-picker mode. A loader command is
// dispatched from Init to fetch projects.
func NewWithPicker(client *kargo.Client, contextName string) Model {
	m := newBase()
	m.client = client
	m.contextName = contextName
	m.phase = phasePickingProject
	m.nsLoading = true
	return m
}

func newBase() Model {
	deploysT := newTable(allStageColumns)
	freightsT := newTable(allFreightColumns)

	ti := textinput.New()
	ti.Prompt = "/"
	ti.Placeholder = "filter…"
	ti.CharLimit = 64

	nsTi := textinput.New()
	nsTi.Prompt = "› "
	nsTi.Placeholder = "type to filter projects…"
	nsTi.CharLimit = 64
	nsTi.Focus()

	vp := viewport.New(viewport.WithHeight(20), viewport.WithWidth(40))
	overlayVP := viewport.New(viewport.WithHeight(20), viewport.WithWidth(80))
	overlayVP.SoftWrap = true
	helpVP := viewport.New(viewport.WithHeight(20), viewport.WithWidth(60))

	return Model{
		view:          viewDeploys,
		deploysTable:  deploysT,
		freightsTable: freightsT,
		filter:        ti,
		nsFilter:      nsTi,
		panelVP:       vp,
		overlayVP:     overlayVP,
		helpVP:        helpVP,
		filterValues:  make(map[view]string),
		sort:          make(map[view]sortMode),
	}
}

func newTable(cols []table.Column) table.Model {
	t := table.New(
		table.WithColumns(cols),
		table.WithFocused(true),
		table.WithHeight(20),
	)
	st := table.DefaultStyles()
	st.Header = st.Header.
		Foreground(normal).Background(bg).Bold(true).
		BorderBottom(true).BorderForeground(muted)
	st.Cell = st.Cell.Foreground(normal).Background(bg)
	// Use Reverse + bold + selected fg color so it stays visible even when
	// per-cell ANSI codes have set their own foreground.
	st.Selected = lipgloss.NewStyle().
		Background(selected).
		Foreground(bg).
		Bold(true).
		Reverse(false)
	t.SetStyles(st)
	return t
}

// Init is the Bubble Tea entry point. It dispatches the initial commands —
// either loading projects for the picker, or starting the refresh ticker
// for an already-selected project — and kicks off Argo CD URL discovery.
func (m Model) Init() tea.Cmd {
	if m.phase == phasePickingProject {
		return tea.Batch(loadProjectsCmd(m.client), textinput.Blink, discoverArgoURLCmd(m.client))
	}
	return tea.Batch(tickCmd(), discoverArgoURLCmd(m.client))
}
