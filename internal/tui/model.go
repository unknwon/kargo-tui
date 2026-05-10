package tui

import (
	"context"
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
	viewTree
	viewGraph
)

type phase int

const (
	phaseRunning phase = iota
	phasePickingProject
	phasePickingContext
)

type overlayMode int

const (
	overlayNone overlayMode = iota
	overlayLogs
	overlayDiff
	overlayPromote
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

	// authExpired is set when a Kargo RPC fails with CodeUnauthenticated
	// after the transport's refresh attempt also failed (or no refresh
	// token was saved). It drives a sticky red banner above the help line
	// and unlocks the `R` shortcut to trigger an inline re-login. Cleared
	// on the next successful tick or after a successful re-login.
	authExpired    bool
	authExpiredMsg string

	// helpVP renders the keybindings overlay body so it can scroll
	// independently of the table/details viewports.
	helpVP viewport.Model

	// Logs/Diff overlay state.
	overlay          overlayMode
	overlayVP        viewport.Model
	overlayTitle     string
	overlayStageName string // remembered so the tick handler can refresh logs
	overlayLoading   bool
	overlayError     error
	overlayPromos    []kargo.PromotionEntry
	overlayEvents    []kargo.EventEntry
	overlayDiffFrom  *kargo.Freight
	overlayDiffTo    *kargo.Freight

	// Project picker state. nsExplicit is true when the picker was opened
	// from a running session (e.g. via the `n` key) rather than at startup,
	// so we don't auto-jump past the picker when only one project exists.
	projects      []string
	projectsError error
	nsFilter      textinput.Model
	nsCursor      int
	nsLoading     bool
	nsExplicit    bool

	// Context picker state. Populated when the user presses `C` to switch
	// between configured Kargo instances. ctxBuilder is supplied by main and
	// returns a fresh client + that context's default project for a chosen
	// context name. ctxNames lists the available context names for display.
	// ctxAdding/ctxURLInput drive the inline "add new instance" subform that
	// kicks off an SSO login when the user presses `+` in the picker.
	ctxNames    []string
	ctxCursor   int
	ctxFilter   textinput.Model
	ctxError    error
	ctxBuilder func(name string) (*kargo.Client, string, error)
	ctxLogin   func(ctx context.Context, url string, status func(string)) (newName string, err error)
	// ctxRelogin re-runs SSO against an already-configured context,
	// preserving its saved insecureSkipTLSVerify / project flags so the
	// re-auth flow doesn't silently strip them. Used by the inline `R`
	// handler when the persistent auth banner is up.
	ctxRelogin func(ctx context.Context, contextName string, status func(string)) (string, error)
	ctxSend    func(tea.Msg) // injected from main so login goroutine can stream status updates
	ctxAdding      bool
	ctxLoggingIn   bool
	ctxLoginStatus string
	ctxLoginCancel context.CancelFunc
	ctxURLInput    textinput.Model

	// Tree view state. treeNodes is the flat list of currently-visible rows
	// (rebuilt on each data refresh and on every expand/collapse). Persists
	// across view switches so navigating away and back keeps your place.
	treeNodes    []treeNode
	treeCursor   int
	treeExpanded map[string]bool

	// Graph view state. graphLayout is recomputed on each data refresh
	// (Sugiyama layered DAG); graphCursor indexes into graphLayout.nodes.
	graphLayout graphLayout
	graphCursor int

	// Promote overlay state.
	promoteStage      string // target stage name
	promoteCandidates []kargo.Freight
	promoteCursor     int
	promoteStep       promoteStep
	promoteResult     string // promotion name on success
	promoteError      error

	// stageWatchCancel cancels the active WatchStages goroutine when the
	// user switches projects or the program exits. nil when no watch is
	// running (initial state, or after a stream error fell back to
	// tick-only refresh).
	stageWatchCancel context.CancelFunc

	width, height int

	loading bool
}

// allStageColumns / allFreightColumns are the full set of columns. Horizontal
// scrolling reveals additional columns by advancing colOffset; the marker
// column at index 0 is always pinned.
var allStageColumns = []table.Column{
	{Title: " ", Width: 2},
	{Title: "Name", Width: 30},
	{Title: "Health", Width: 14},
	{Title: "Argo", Width: 22},
	{Title: "Last Promo", Width: 12},
	{Title: "Freight", Width: 32},
	{Title: "Age", Width: 8},
	{Title: "Shard", Width: 10},
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

// SetSendMsg is a tea.Msg that injects a thread-safe message sender into
// the running model so background goroutines (notably the SSO login flow)
// can stream status updates into the TUI. Sent by main after constructing
// the tea.Program.
type SetSendMsg struct{ Send func(tea.Msg) }

// noteAuthFailure sets the sticky auth-expired banner from an RPC error,
// but only when the error is actually an auth failure. Non-auth errors
// (network blips, server 5xx) are left to their existing handlers.
func (m *Model) noteAuthFailure(err error) {
	if !kargo.IsUnauthenticated(err) {
		return
	}
	m.authExpired = true
	m.authExpiredMsg = err.Error()
}

// noteAuthSuccess clears the auth-expired banner after a successful RPC.
// When the banner was up *and* the watch is currently dead, it also
// restarts the watch — covering the common case where a 401 tore the
// stream down and a subsequent successful tick (post-refresh or
// post-relogin) means the new token is good. Watches that died for
// non-auth reasons (network blip, proxy) are left alone here so we don't
// retry-storm; the existing tick-only fallback keeps the UI working.
func (m *Model) noteAuthSuccess() {
	if !m.authExpired {
		return
	}
	m.authExpired = false
	m.authExpiredMsg = ""
	if m.stageWatchCancel == nil {
		m.restartStageWatch()
	}
}

// restartStageWatch stops any in-flight WatchStages goroutine and
// starts a fresh one for the current project. No-op when ctxSend isn't
// wired yet (the watch needs a way to post events back, so we skip
// starting until SetSendMsg has arrived).
func (m *Model) restartStageWatch() {
	if m.stageWatchCancel != nil {
		m.stageWatchCancel()
		m.stageWatchCancel = nil
	}
	if m.ctxSend == nil || m.project == "" || m.client == nil {
		return
	}
	m.stageWatchCancel = startStageWatchGoroutine(m.client, m.project, m.ctxSend)
}

// WithAuthExpired starts the model with the session-expired banner up.
// Used by main when the saved id_token's `exp` claim has already passed
// at startup, so the user sees the prompt before any RPC has had a
// chance to fire and confirm it via 401. A successful tick clears it
// the moment a (proactively-refreshed) bearer starts working.
func (m Model) WithAuthExpired(msg string) Model {
	m.authExpired = true
	if msg != "" {
		m.authExpiredMsg = msg
	}
	return m
}

// WithContexts wires in the list of configured Kargo contexts, a builder
// that returns a fresh client for a chosen context, a login callback that
// authenticates a *new* Kargo URL via SSO, and a relogin callback that
// re-authenticates an *existing* context (preserving its saved flags).
// When set, pressing `C` inside the TUI opens the context picker; `+`
// inside the picker triggers the login flow for a new URL; `R` from the
// auth-expired banner triggers the relogin flow for the current context.
func (m Model) WithContexts(
	names []string,
	build func(name string) (*kargo.Client, string, error),
	login func(ctx context.Context, url string, status func(string)) (string, error),
	relogin func(ctx context.Context, contextName string, status func(string)) (string, error),
) Model {
	m.ctxNames = names
	m.ctxBuilder = build
	m.ctxLogin = login
	m.ctxRelogin = relogin
	ti := newInput("› ", "type to filter contexts…", 64)
	m.ctxFilter = ti
	urlIn := newInput("URL › ", "https://kargo.example.com", 256)
	m.ctxURLInput = urlIn
	return m
}

// newInput builds a textinput.Model with kargo-tui's preferred cursor style:
// a thin bar instead of the default white block, no blink. The default
// virtual cursor reverses fg/bg on the cell under the cursor, which looks
// like a stray "selection" highlight and confused early users.
//
// Width must be set: textinput's placeholder rendering clips to Width+1
// runes, so an unset Width shows only the first letter of the placeholder
// (e.g. "t" or "h" instead of the full hint).
func newInput(prompt, placeholder string, charLimit int) textinput.Model {
	ti := textinput.New()
	ti.Prompt = prompt
	ti.Placeholder = placeholder
	ti.CharLimit = charLimit
	ti.SetWidth(80)
	styles := ti.Styles()
	styles.Cursor.Color = selected
	styles.Cursor.Shape = tea.CursorBar
	styles.Cursor.Blink = false
	ti.SetStyles(styles)
	ti.SetVirtualCursor(false)
	return ti
}

func newBase() Model {
	deploysT := newTable(allStageColumns)
	freightsT := newTable(allFreightColumns)

	ti := newInput("/", "filter…", 64)

	nsTi := newInput("› ", "type to filter projects…", 64)
	nsTi.Focus()

	vp := viewport.New(viewport.WithHeight(20), viewport.WithWidth(40))
	overlayVP := viewport.New(viewport.WithHeight(20), viewport.WithWidth(80))
	overlayVP.SoftWrap = true
	helpVP := viewport.New(viewport.WithHeight(20), viewport.WithWidth(60))
	helpVP.SoftWrap = true

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
