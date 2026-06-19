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
	darkFg      = lipgloss.Color("#070707") // for reverse-video fg-on-bright cases
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

// String returns a stable name for trace attributes and debug logs.
func (v view) String() string {
	switch v {
	case viewDeploys:
		return "deploys"
	case viewFreights:
		return "freights"
	case viewControlFlow:
		return "controlFlow"
	case viewTree:
		return "tree"
	case viewGraph:
		return "graph"
	}
	return "unknown"
}

type phase int

const (
	phaseRunning phase = iota
	phasePickingProject
	phasePickingContext
)

func (p phase) String() string {
	switch p {
	case phaseRunning:
		return "running"
	case phasePickingProject:
		return "pickingProject"
	case phasePickingContext:
		return "pickingContext"
	}
	return "unknown"
}

type overlayMode int

const (
	overlayNone overlayMode = iota
	overlayLogs
	overlayDiff
	overlayPromote
)

type logsTab int

const (
	logsTabPromotions logsTab = iota
	logsTabEvents
)

type helpTab int

const (
	helpTabKeybindings helpTab = iota
	helpTabInfo
	helpTabCount
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

	// showHelp renders a full-screen help overlay.
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

	// Snapshot of the cells last pushed to each table, indexed the same as
	// visibleDeploys/visibleFreights. The rebuild-skip check compares the
	// freshly built rows against these — a render-faithful equality check, so
	// any cell that's actually shown to the user participates in the
	// comparison. Don't reintroduce a struct-field equality function: it
	// drifts out of sync the moment a cell renderer reads anything new.
	lastDeployRows  []table.Row
	lastFreightRows []table.Row

	// Horizontal scroll offset (in column count) per table view.
	deploysColOffset  int
	freightsColOffset int

	// Listview scroll-coalescing state. Mouse wheel events arrive every
	// ~15ms during sustained scroll; each one normally triggers a bubbles
	// UpdateViewport, an applyCursorMarker pass, and a full bubbletea
	// View()/paintFrame, totalling ~15ms of work — right at the input
	// rate, so the input queue saturates and scroll feels choppy. The
	// coalesce path accumulates the delta and flushes once per ~16ms tick
	// (60fps ceiling) so the user pays one render for every burst of
	// notches that lands within a frame instead of one render per notch.
	pendingScroll      int
	scrollFlushPending bool

	// filter is the live textinput; filterValues persists each view's filter
	// string so switching views restores the per-list query.
	filter       textinput.Model
	filtering    bool
	filterValues map[view]string
	sort         map[view]sortMode
	argoShards   kargo.ArgoCDShards
	// argoShardsCache memoizes shard tables by context name so a
	// switch back to a previously-visited context picks the URLs up
	// instantly instead of re-paying the GetConfig round trip.
	// Populated synchronously at startup and on every context switch.
	argoShardsCache map[string]kargo.ArgoCDShards
	yankedMessage   string
	yankedAt        time.Time
	// yankedIsError marks the transient toast as a failure so the status
	// line renders it red instead of the green used for successful
	// yank/open notifications.
	yankedIsError bool

	// authExpired is set when a Kargo RPC fails with CodeUnauthenticated
	// after the transport's refresh attempt also failed (or no refresh
	// token was saved). It drives a sticky red banner above the help line
	// and unlocks the `R` shortcut to trigger an inline re-login. Cleared
	// on the next successful tick or after a successful re-login.
	authExpired    bool
	authExpiredMsg string
	// autoReloginTried latches once the cold-start auto-SSO has fired so a
	// failed/cancelled auto-login doesn't loop. After that the user drives
	// recovery manually with R.
	autoReloginTried bool

	// helpVP renders the help overlay body so it can scroll
	// independently of the table/details viewports.
	helpVP  viewport.Model
	helpTab helpTab

	// Logs/Diff overlay state.
	overlay          overlayMode
	overlayVP        viewport.Model
	overlayTitle     string
	overlayStageName string // remembered so the tick handler can refresh logs
	overlayLoading   bool
	overlayError     error
	overlayPromos    []kargo.PromotionEntry
	overlayEvents    []kargo.EventEntry
	overlayLogsTab   logsTab
	overlayDiffFrom  *kargo.Freight
	overlayDiffTo    *kargo.Freight

	// Project picker state. nsExplicit is true when the picker was opened
	// from a running session (e.g. via the `n` key) rather than at startup,
	// so we don't auto-jump past the picker when only one project exists.
	projects      []string
	projectsError error
	nsFilter      textinput.Model
	nsCursor      int
	nsScroll      int
	nsLoading     bool
	nsExplicit    bool

	// Context picker state. Populated when the user presses `C` to switch
	// between configured Kargo instances. ctxBuilder is supplied by main and
	// returns a fresh client + that context's default project for a chosen
	// context name. ctxNames lists the available context names for display.
	// ctxAdding/ctxURLInput drive the inline "add new instance" subform that
	// kicks off an SSO login when the user presses `+` in the picker.
	ctxNames   []string
	ctxCursor  int
	ctxScroll  int
	ctxFilter  textinput.Model
	ctxError   error
	ctxBuilder func(name string) (*kargo.Client, string, error)
	ctxLogin   func(ctx context.Context, url string, status func(string)) (newName string, err error)
	// ctxRelogin re-runs SSO against an already-configured context,
	// preserving its saved insecureSkipTLSVerify / project flags so the
	// re-auth flow doesn't silently strip them. Used by the inline `R`
	// handler when the persistent auth banner is up.
	ctxRelogin func(ctx context.Context, contextName string, status func(string)) (string, error)
	ctxDelete  func(name string) error
	// ctxPersistProject saves the active project back to the named
	// context's config so the next cold start reopens it. Invoked whenever
	// the running phase begins for a project (picker selection or a context
	// switch that lands on a single project).
	ctxPersistProject func(contextName, project string)
	ctxSend           func(tea.Msg) // injected from main so login goroutine can stream status updates
	ctxAdding         bool
	ctxLoggingIn      bool
	ctxLoginStatus    string
	ctxLoginCancel    context.CancelFunc
	ctxURLInput       textinput.Model
	// ctxDeleting holds the name of the context awaiting a delete
	// confirmation. Empty when no confirmation is in flight. Set when the
	// user presses `D` in the picker browse mode; cleared on y/n/esc.
	ctxDeleting string

	// Tree view state. treeNodes is the flat list of currently-visible rows
	// (rebuilt on each data refresh and on every expand/collapse). Persists
	// across view switches so navigating away and back keeps your place.
	treeNodes    []treeNode
	treeCursor   int
	treeExpanded map[string]bool
	// treeScroll is the row index at the top of the visible tree window.
	// Sticky across renders so the viewport only shifts when the cursor
	// would otherwise leave it, mirroring the graph view's pan behavior.
	treeScroll int

	// Graph view state. graphLayout is recomputed on each data refresh
	// (Sugiyama layered DAG); graphCursor indexes into graphLayout.nodes.
	graphLayout graphLayout
	graphCursor int
	// graphPanX/Y is the canvas-coordinate top-left the renderer pans to.
	// Sticky across renders so the viewport only shifts when the cursor
	// would otherwise leave it, avoiding a jump every time the cursor
	// moves between already-visible nodes.
	graphPanX int
	graphPanY int

	// graphLayoutVersion is bumped by rebuildGraph; the graphRender cache
	// keys off it so a fresh layout invalidates the cached rendered
	// string. graphRender is pointer-typed so the cache survives Model
	// value copies.
	graphLayoutVersion int
	graphRender        *graphRenderCache

	// graphExpanded controls how much each stage box shows. False (the
	// default) renders compact boxes mirroring the Kargo web UI: freight
	// SHA + alias + age, with state conveyed by the border colour. True
	// restores the full Health/Argo/Sync/Promo rows. The selected stage's
	// complete detail always lives in the side panel regardless.
	graphExpanded bool

	// Graph-view name search. `/` opens m.filter as usual; in graph view
	// the filter doesn't hide nodes (that would break the DAG layout)
	// but instead drives a search that jumps the cursor to the first
	// matching stage and remembers the full match list so n/N step
	// through them after the search is committed with enter.
	// graphSearchSaved snapshots the pre-search cursor so esc restores
	// it; graphSearchActive distinguishes "searching, query is empty"
	// from "no search in progress".
	graphSearchMatches []int
	graphSearchPos     int
	graphSearchSaved   int
	graphSearchActive  bool

	// Promote overlay state.
	promoteStage      string // target stage name (also: source stage when promoteDownstream is true)
	promoteCandidates []promoteCandidate
	promoteCursor     int
	promoteScroll     int
	promoteStep       promoteStep
	promoteResult     string // promotion name on success
	promoteError      error
	// promoteDownstream switches the overlay's submit action from
	// PromoteToStage (promote freight into promoteStage) to
	// PromoteDownstream (promote chosen freight from promoteStage to every
	// downstream subscriber). Set by `>` whenever the downstream overlay
	// opens; cleared by `P` / openPromoteOverlay.
	promoteDownstream bool

	// stageWatchCancel cancels the active WatchStages goroutine when the
	// user switches projects or the program exits. nil when no watch is
	// running (initial state, or after a stream error fell back to
	// tick-only refresh).
	stageWatchCancel context.CancelFunc

	width, height int

	loading bool

	// panicMessage holds a captured panic + stack trace so the View can
	// surface it as a copyable popup instead of letting the program tear
	// down with the alt-screen still active. Cleared by pressing esc on
	// the popup. panicVP is a scrollable viewport over the trace.
	panicMessage string
	panicVP      viewport.Model

	// Right-click context menu state. menuOpen toggles the floating
	// menu; menuX/Y anchor it in screen cells; menuItems lists the
	// actions for the right-clicked target; menuCursor is the keyboard
	// cursor inside the menu. Each menu item's action closure captures
	// the target it was built for, so the chosen action runs against
	// that specific stage/freight regardless of cursor movement.
	menuOpen   bool
	menuX      int
	menuY      int
	menuItems  []menuItem
	menuCursor int

	// refreshingWarehouses gates the R warehouse-reconcile shortcut so
	// rapid presses don't fan out concurrent List+Refresh goroutines.
	// Cleared in the warehousesRefreshedMsg handler regardless of
	// success or error.
	refreshingWarehouses bool

	// mouseEnabled toggles mouse capture on. Off by default so the
	// terminal keeps native text selection and scrollback. Bound to "M"
	// globally.
	mouseEnabled bool

	// theme is the active palette, toggled by `T` and seeded once at
	// startup from the OSC 11 background-color probe in main.
	theme themeMode
}

// menuItem is one row in the right-click context menu. Label is shown;
// action is invoked when the user picks the row.
type menuItem struct {
	label  string
	action func(*Model) tea.Cmd
}

// allStageColumns / allFreightColumns are the full set of columns. Horizontal
// scrolling reveals additional columns by advancing colOffset; the marker
// column at index 0 is always pinned.
var allStageColumns = []table.Column{
	{Title: " ", Width: 2},
	{Title: "Name", Width: 30},
	{Title: "Health", Width: 14},
	{Title: "Argo", Width: 22},
	{Title: "Last Promo", Width: 16},
	{Title: "Freight", Width: 32},
	{Title: "Age", Width: 8},
	{Title: "Shard", Width: 10},
}

var allFreightColumns = []table.Column{
	{Title: " ", Width: 2},
	{Title: "Name", Width: 30},
	{Title: "Age", Width: 8},
	{Title: "Current", Width: 10},
	{Title: "Verified", Width: 10},
	{Title: "Approved", Width: 10},
	{Title: "Warehouse", Width: 30},
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
	// Pre-Run path: the first refreshRows happens before bubbletea starts
	// an Update, so there's no live span to parent off. Background here
	// makes the initial refreshRows a top-level trace boundary.
	m.refreshRows(context.Background())
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

// fitTablesToWindow propagates the most recent terminal size onto the
// deploys/freights tables. Called from picker→running transitions
// because the picker-phase WindowSizeMsg handler only stores width and
// height on the model and skips the table resize. Without this the
// first frame after the transition uses the bubbles default table
// dimensions and clips every row out of view. No-op when no size
// message has arrived yet.
func (m *Model) fitTablesToWindow() {
	if m.width <= 0 {
		return
	}
	h := m.height - 4
	if h < 3 {
		h = 3
	}
	m.deploysTable.SetHeight(h)
	m.deploysTable.SetWidth(m.width)
	m.freightsTable.SetHeight(h)
	m.freightsTable.SetWidth(m.width)
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
// inside the picker triggers the login flow for a new URL; `D` removes the
// highlighted context after an inline confirm; `R` from the auth-expired
// banner triggers the relogin flow for the current context.
func (m Model) WithContexts(
	names []string,
	build func(name string) (*kargo.Client, string, error),
	login func(ctx context.Context, url string, status func(string)) (string, error),
	relogin func(ctx context.Context, contextName string, status func(string)) (string, error),
	del func(name string) error,
	persistProject func(contextName, project string),
) Model {
	m.ctxNames = names
	m.ctxBuilder = build
	m.ctxLogin = login
	m.ctxRelogin = relogin
	m.ctxDelete = del
	m.ctxPersistProject = persistProject
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
	panicVP := viewport.New(viewport.WithHeight(20), viewport.WithWidth(80))
	panicVP.SoftWrap = false

	return Model{
		view:          viewDeploys,
		deploysTable:  deploysT,
		freightsTable: freightsT,
		filter:        ti,
		nsFilter:      nsTi,
		panelVP:       vp,
		overlayVP:     overlayVP,
		helpVP:        helpVP,
		panicVP:       panicVP,
		filterValues:  make(map[view]string),
		// The stage list views (deploys, control flow) default to name
		// ascending. The freights view keeps the client's newest-first
		// ordering via the zero-value sortDefault.
		sort: map[view]sortMode{
			viewDeploys:     sortByName,
			viewControlFlow: sortByName,
		},
		graphRender: &graphRenderCache{},
	}
}

func newTable(cols []table.Column) table.Model {
	t := table.New(
		table.WithColumns(cols),
		table.WithFocused(true),
		table.WithHeight(20),
	)
	restyleTable(&t)
	return t
}

// restyleTable re-applies the theme-dependent style block to t. Called
// both from newTable at construction and from setTheme on every toggle,
// because table.Styles bakes color values in at SetStyles time.
func restyleTable(t *table.Model) {
	st := table.DefaultStyles()
	st.Header = st.Header.
		Foreground(normal).Background(bg).Bold(true).
		BorderBottom(true).BorderForeground(muted)
	st.Cell = st.Cell.Foreground(normal).Background(bg)
	// Use Reverse + bold + selected fg color so it stays visible even when
	// per-cell ANSI codes have set their own foreground.
	st.Selected = lipgloss.NewStyle().
		Background(selected).
		Foreground(darkFg).
		Bold(true).
		Reverse(false)
	t.SetStyles(st)
}

// Init is the Bubble Tea entry point. It dispatches the initial commands —
// either loading projects for the picker, or starting the refresh ticker
// for an already-selected project, and kicks off Argo CD shard discovery.
func (m Model) Init() tea.Cmd {
	if m.phase == phasePickingProject {
		return tea.Batch(loadProjectsCmd(m.client), textinput.Blink)
	}
	return tickCmd()
}

// WithArgoShards preloads the discovered shard table on the model so
// the panel can render Argo links from the very first frame instead
// of waiting on an async discovery cmd that may never land. The cache
// stores the result under contextName for fast restore on
// context-switch.
func (m Model) WithArgoShards(contextName string, shards kargo.ArgoCDShards) Model {
	m.argoShards = shards
	if m.argoShardsCache == nil {
		m.argoShardsCache = make(map[string]kargo.ArgoCDShards)
	}
	// Don't cache an empty result. An empty map usually means discovery
	// failed, and caching it would block re-discovery on later context
	// switches, leaving argo links missing until a restart.
	if contextName != "" && len(shards) > 0 {
		m.argoShardsCache[contextName] = shards
	}
	return m
}
