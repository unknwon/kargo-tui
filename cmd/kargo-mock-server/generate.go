package main

import (
	"fmt"
	"math/rand/v2"
	"strings"
	"time"

	kargoapi "github.com/akuity/kargo/api/v1alpha1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// buildProjectState turns a topology into a fully populated projectState:
// builds Stage protos, generates freight history, simulates promotions
// rolling through the DAG to fill in stage current-freight assignments,
// and lays down events along the way.
func buildProjectState(t *topology, rng *rand.Rand) *projectState {
	ps := &projectState{
		name:     t.Project,
		topology: t,
		stages:   make(map[string]*kargoapi.Stage, len(t.Stages)),
		freight:  make(map[string]*kargoapi.Freight, 256),
	}

	// Project creation moment: 180 days ago.
	projectCreated := time.Now().Add(-180 * 24 * time.Hour).UTC()

	// 1. Build Stage protos.
	for i, st := range t.Stages {
		stageCreated := projectCreated.Add(time.Duration(i) * 30 * time.Minute)
		ps.stages[st.Name] = buildStage(t.Project, st, stageCreated, rng)
	}

	// 2. Generate freight. Each warehouse emits freight at a steady cadence.
	freightPerProject := freightTargetFor(len(t.Stages))
	perWarehouse := freightPerProject / len(t.Warehouses)
	if perWarehouse < 1 {
		perWarehouse = 1
	}
	var orderedFreight []*kargoapi.Freight
	for _, wh := range t.Warehouses {
		for i := 0; i < perWarehouse; i++ {
			// Spread freight uniformly across the 180-day window, oldest
			// first so simulation can replay it in order.
			created := projectCreated.Add(time.Duration(float64(i)/float64(perWarehouse)*180*24) * time.Hour)
			f := buildFreight(t.Project, wh, created, rng)
			ps.freight[f.Name] = f
			orderedFreight = append(orderedFreight, f)
		}
	}

	// 3. Simulate promotions. Walk freight oldest-first; promote each into
	// every stage that subscribes to its warehouse, then cascade through
	// downstream stages. We compress simulated time so the whole 180 days
	// of activity fits naturally.
	promoCount := simulatePromotions(ps, orderedFreight, rng)

	// 3b. Sprinkle in a handful of in-flight Running promotions so a few
	// stages show up as currently promoting in the deploys view.
	sprinkleInFlightPromotions(ps, orderedFreight, rng)

	// 4. Emit a sprinkling of project-level events.
	emitInitialEvents(ps, rng, projectCreated)

	_ = promoCount
	return ps
}

// freightTargetFor returns how many pieces of freight to fabricate based
// on stage count. Tuned for the headline scale targets.
func freightTargetFor(stageCount int) int {
	switch {
	case stageCount >= 100:
		return 500
	case stageCount >= 60:
		return 300
	default:
		return 150
	}
}

// stageHealthState picks an initial health/sync state combo for a deploy
// stage so the dashboard isn't a wall of green. Weighted to keep most
// things healthy while leaving room for visible variety.
type stageHealthState struct {
	stageHealth kargoapi.HealthState
	issues      []string
	argoHealth  string // empty = stage has no Argo app at all
	argoSync    string
	hasAppInfo  bool
}

func pickStageHealth(rng *rand.Rand, isControlFlow bool) stageHealthState {
	if isControlFlow {
		// Control-flow stages don't deploy so no Argo info, but the TUI
		// still shows their health (always Healthy is fine; they aggregate
		// from downstream in real Kargo, but the TUI doesn't compute that).
		return stageHealthState{stageHealth: kargoapi.HealthStateHealthy}
	}
	roll := rng.Float64()
	switch {
	case roll < 0.70:
		return stageHealthState{
			stageHealth: kargoapi.HealthStateHealthy,
			argoHealth:  "Healthy", argoSync: "Synced", hasAppInfo: true,
		}
	case roll < 0.80:
		return stageHealthState{
			stageHealth: kargoapi.HealthStateProgressing,
			issues:      []string{"Sync in progress"},
			argoHealth:  "Progressing", argoSync: "OutOfSync", hasAppInfo: true,
		}
	case roll < 0.88:
		issue := []string{
			"Argo CD Application reports Degraded",
			"Readiness probe failing on 3/12 pods",
			"Image pull error: ImagePullBackOff",
			"CrashLoopBackOff: liveness check failed",
		}[rng.IntN(4)]
		return stageHealthState{
			stageHealth: kargoapi.HealthStateUnhealthy,
			issues:      []string{issue},
			argoHealth:  "Degraded", argoSync: "OutOfSync", hasAppInfo: true,
		}
	case roll < 0.93:
		return stageHealthState{
			stageHealth: kargoapi.HealthStateProgressing,
			argoHealth:  "Progressing", argoSync: "Synced", hasAppInfo: true,
		}
	case roll < 0.98:
		return stageHealthState{
			stageHealth: kargoapi.HealthStateUnknown,
			argoHealth:  "Unknown", argoSync: "Unknown", hasAppInfo: true,
		}
	default:
		// No Argo app info at all — shows "—" in the Argo column.
		return stageHealthState{stageHealth: kargoapi.HealthStateHealthy}
	}
}

// buildStage constructs a kargoapi.Stage with valid Spec and an empty
// Status. simulatePromotions populates Status later.
func buildStage(project string, st stageTopo, created time.Time, rng *rand.Rand) *kargoapi.Stage {
	reqFreight := make([]kargoapi.FreightRequest, 0, len(st.Warehouses)+len(st.Upstreams))
	// Direct subscriptions to warehouses.
	for _, wh := range st.Warehouses {
		reqFreight = append(reqFreight, kargoapi.FreightRequest{
			Origin: kargoapi.FreightOrigin{
				Kind: kargoapi.FreightOriginKindWarehouse,
				Name: wh,
			},
			Sources: kargoapi.FreightSources{Direct: true},
		})
	}
	// Upstream stage references — group them under a single FreightRequest
	// per upstream, all pointing at the same warehouse origin (we use the
	// project's first warehouse as a placeholder; the TUI only reads the
	// Stages slice for graph wiring).
	if len(st.Upstreams) > 0 {
		reqFreight = append(reqFreight, kargoapi.FreightRequest{
			Origin: kargoapi.FreightOrigin{
				Kind: kargoapi.FreightOriginKindWarehouse,
				Name: "wh-placeholder",
			},
			Sources: kargoapi.FreightSources{Stages: append([]string{}, st.Upstreams...)},
		})
	}

	// Deploy stages get a PromotionTemplate; control-flow stages don't,
	// so kargoapi.Stage.IsControlFlow() picks them up as routing nodes
	// in the TUI graph.
	var tpl *kargoapi.PromotionTemplate
	if !st.ControlFlow {
		tpl = &kargoapi.PromotionTemplate{
			Spec: kargoapi.PromotionTemplateSpec{
				Steps: []kargoapi.PromotionStep{
					{Uses: "git-clone"},
					{Uses: "kustomize-set-image"},
					{Uses: "argocd-update"},
				},
			},
		}
	}

	state := pickStageHealth(rng, st.ControlFlow)
	health := &kargoapi.Health{Status: state.stageHealth, Issues: state.issues}
	if state.hasAppInfo {
		appName := st.Name
		appNS := "argocd"
		blob := []byte(fmt.Sprintf(
			`[{"applicationStatuses":[{"namespace":%q,"name":%q,"health":{"status":%q},"sync":{"status":%q}}]}]`,
			appNS, appName, state.argoHealth, state.argoSync,
		))
		health.Output = &apiextensionsv1.JSON{Raw: blob}
	}

	return &kargoapi.Stage{
		TypeMeta: metav1.TypeMeta{Kind: "Stage", APIVersion: "kargo.akuity.io/v1alpha1"},
		ObjectMeta: metav1.ObjectMeta{
			Name:              st.Name,
			Namespace:         project,
			CreationTimestamp: metav1.NewTime(created),
		},
		Spec: kargoapi.StageSpec{
			Shard:             st.Shard,
			RequestedFreight:  reqFreight,
			PromotionTemplate: tpl,
		},
		Status: kargoapi.StageStatus{
			Health: health,
		},
	}
}

// buildFreight constructs a single Freight with realistic commits/images.
func buildFreight(project string, wh warehouseTopo, created time.Time, rng *rand.Rand) *kargoapi.Freight {
	id := fmt.Sprintf("%010x", rng.Uint64()&0xFFFFFFFFFF)
	name := fmt.Sprintf("f-%s", id)

	alias := makeAlias(rng)

	commits := make([]kargoapi.GitCommit, 0, len(wh.Git))
	for _, g := range wh.Git {
		commits = append(commits, kargoapi.GitCommit{
			RepoURL: g.RepoURL,
			Branch:  defaultBranch(g.Branch),
			ID:      randSHA(rng),
			Message: pickCommitMessage(rng),
			Author:  pickAuthor(rng),
		})
	}
	images := make([]kargoapi.Image, 0, len(wh.Img))
	for _, im := range wh.Img {
		images = append(images, kargoapi.Image{
			RepoURL: im.RepoURL,
			Tag:     fmt.Sprintf("v%d.%d.%d", rng.IntN(3)+1, rng.IntN(20), rng.IntN(50)),
			Digest:  "sha256:" + randSHA(rng) + randSHA(rng)[:24],
		})
	}

	return &kargoapi.Freight{
		TypeMeta: metav1.TypeMeta{Kind: "Freight", APIVersion: "kargo.akuity.io/v1alpha1"},
		ObjectMeta: metav1.ObjectMeta{
			Name:              name,
			Namespace:         project,
			CreationTimestamp: metav1.NewTime(created),
		},
		Alias: alias,
		Origin: kargoapi.FreightOrigin{
			Kind: kargoapi.FreightOriginKindWarehouse,
			Name: wh.Name,
		},
		Commits: commits,
		Images:  images,
		Status: kargoapi.FreightStatus{
			VerifiedIn:  make(map[string]kargoapi.VerifiedStage),
			ApprovedFor: make(map[string]kargoapi.ApprovedStage),
			CurrentlyIn: make(map[string]kargoapi.CurrentStage),
		},
	}
}

// simulatePromotions replays freight through the DAG to produce a
// realistic promotion history and final stage states.
func simulatePromotions(ps *projectState, freight []*kargoapi.Freight, rng *rand.Rand) int {
	t := ps.topology

	// Index stages by warehouse subscription and by upstream relationship.
	bySubscribedWarehouse := make(map[string][]string)
	for _, st := range t.Stages {
		for _, wh := range st.Warehouses {
			bySubscribedWarehouse[wh] = append(bySubscribedWarehouse[wh], st.Name)
		}
	}

	count := 0
	for _, f := range freight {
		entryStages := bySubscribedWarehouse[f.Origin.Name]
		if len(entryStages) == 0 {
			continue
		}
		// Walk BFS from each entry stage. Not every freight reaches every
		// downstream — sample some chance per stage so older freight has
		// patchy coverage and the activity log looks organic.
		t0 := f.CreationTimestamp.Time
		visited := make(map[string]struct{})
		queue := append([]string{}, entryStages...)
		stageHop := 0
		for len(queue) > 0 {
			next := queue[0]
			queue = queue[1:]
			if _, dup := visited[next]; dup {
				continue
			}
			visited[next] = struct{}{}
			// 85% chance to actually promote.
			if rng.Float64() > 0.85 {
				continue
			}
			// Each hop is ~30 minutes + jitter.
			t0 = t0.Add(time.Duration(30+rng.IntN(30)) * time.Minute)
			recordHistoricalPromotion(ps, next, f, t0)
			count++
			stageHop++
			// Enqueue downstreams.
			queue = append(queue, t.downstreamsOf(next)...)
		}
		_ = stageHop
	}
	return count
}

// recordHistoricalPromotion appends a completed Succeeded promotion and
// rolls the freight into the stage's current state.
func recordHistoricalPromotion(ps *projectState, stageName string, f *kargoapi.Freight, when time.Time) {
	stage, ok := ps.stages[stageName]
	if !ok {
		return
	}
	promoName := fmt.Sprintf("%s.%s.%s", stageName, randULIDLike(when), shortName(f.Name))
	started := when
	finished := when.Add(2 * time.Minute)
	st := metav1.NewTime(started)
	fn := metav1.NewTime(finished)

	promo := &kargoapi.Promotion{
		TypeMeta: metav1.TypeMeta{Kind: "Promotion", APIVersion: "kargo.akuity.io/v1alpha1"},
		ObjectMeta: metav1.ObjectMeta{
			Name:              promoName,
			Namespace:         ps.name,
			CreationTimestamp: metav1.NewTime(when),
		},
		Spec: kargoapi.PromotionSpec{
			Stage:   stageName,
			Freight: f.Name,
		},
		Status: kargoapi.PromotionStatus{
			Phase:      kargoapi.PromotionPhaseSucceeded,
			StartedAt:  &st,
			FinishedAt: &fn,
		},
	}
	ps.promotions = append([]*kargoapi.Promotion{promo}, ps.promotions...)

	// Update stage to reflect this promotion. Only overwrite LastPromotion
	// if this one is newer.
	if stage.Status.LastPromotion == nil ||
		stage.Status.LastPromotion.FinishedAt == nil ||
		stage.Status.LastPromotion.FinishedAt.Time.Before(finished) {
		stage.Status.LastPromotion = &kargoapi.PromotionReference{
			Name:       promo.Name,
			Freight:    refForFreight(f),
			Status:     &kargoapi.PromotionStatus{Phase: kargoapi.PromotionPhaseSucceeded, FinishedAt: &fn},
			FinishedAt: &fn,
		}
		coll := &kargoapi.FreightCollection{}
		coll.UpdateOrPush(*refForFreight(f))
		stage.Status.FreightHistory = kargoapi.FreightHistory{coll}
		stage.Status.FreightSummary = f.Name
	}
	f.Status.AddVerifiedStage(stageName, finished)
	f.Status.AddCurrentStage(stageName, finished)
}

// sprinkleInFlightPromotions picks ~4-6 random deploy stages and gives
// them a Pending/Running promotion so the dashboard shows live activity
// at boot, before background motion kicks in.
func sprinkleInFlightPromotions(ps *projectState, freight []*kargoapi.Freight, rng *rand.Rand) {
	if len(freight) == 0 {
		return
	}
	deployStages := make([]*kargoapi.Stage, 0, len(ps.stages))
	for _, s := range ps.stages {
		if !s.IsControlFlow() {
			deployStages = append(deployStages, s)
		}
	}
	if len(deployStages) == 0 {
		return
	}
	count := 4 + rng.IntN(3)
	now := time.Now().UTC()
	for i := 0; i < count; i++ {
		stage := deployStages[rng.IntN(len(deployStages))]
		f := freight[rng.IntN(len(freight))]
		phase := kargoapi.PromotionPhaseRunning
		if rng.Float64() < 0.4 {
			phase = kargoapi.PromotionPhasePending
		}
		promoName := fmt.Sprintf("%s.%s.%s", stage.Name, randULIDLike(now), shortName(f.Name))
		started := metav1.NewTime(now.Add(-time.Duration(rng.IntN(120)) * time.Second))
		promo := &kargoapi.Promotion{
			TypeMeta:   metav1.TypeMeta{Kind: "Promotion", APIVersion: "kargo.akuity.io/v1alpha1"},
			ObjectMeta: metav1.ObjectMeta{Name: promoName, Namespace: ps.name, CreationTimestamp: started},
			Spec:       kargoapi.PromotionSpec{Stage: stage.Name, Freight: f.Name},
			Status:     kargoapi.PromotionStatus{Phase: phase, StartedAt: &started},
		}
		ps.promotions = append([]*kargoapi.Promotion{promo}, ps.promotions...)
		ref := &kargoapi.PromotionReference{
			Name:    promo.Name,
			Freight: refForFreight(f),
			Status:  &kargoapi.PromotionStatus{Phase: phase},
		}
		stage.Status.CurrentPromotion = ref
		// Also override LastPromotion so the deploys column shows the
		// in-flight phase. The TUI reads LastPromotion.Status.Phase.
		stage.Status.LastPromotion = ref
	}
}

func emitInitialEvents(ps *projectState, rng *rand.Rand, base time.Time) {
	// Emit one creation event per stage and a few promotion events to
	// give the activity log a head start.
	for _, st := range ps.stages {
		ps.events = append(ps.events, newEvent("Stage", st.Name, "Created",
			fmt.Sprintf("Stage %s created", st.Name), st.CreationTimestamp.Time))
	}
	for i, pr := range ps.promotions {
		if i >= 50 {
			break
		}
		when := base.Add(time.Duration(i) * time.Hour)
		ps.events = append(ps.events, newEvent("Promotion", pr.Name, "PromotionSucceeded",
			fmt.Sprintf("Promotion to %s succeeded", pr.Spec.Stage), when))
	}
	_ = rng
}

// --- naming helpers ---

func makeAlias(rng *rand.Rand) string {
	adj := aliasAdjectives[rng.IntN(len(aliasAdjectives))]
	noun := aliasNouns[rng.IntN(len(aliasNouns))]
	return fmt.Sprintf("%s-%s-%d", adj, noun, rng.IntN(100))
}

func defaultBranch(b string) string {
	if b == "" {
		return "main"
	}
	return b
}

func randSHA(rng *rand.Rand) string {
	const hex = "0123456789abcdef"
	b := make([]byte, 40)
	for i := range b {
		b[i] = hex[rng.IntN(16)]
	}
	return string(b)
}

func randULIDLike(when time.Time) string {
	// 26 chars total: 10-char timestamp + 16-char randomness, Crockford
	// base32 alphabet so the TUI's ulidTimeFromName can pull a real ts out.
	const alphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"
	ms := uint64(when.UnixMilli())
	var b [26]byte
	for i := 9; i >= 0; i-- {
		b[i] = alphabet[ms&0x1F]
		ms >>= 5
	}
	for i := 10; i < 26; i++ {
		b[i] = alphabet[(uint64(when.UnixNano())+uint64(i)*1103515245)&0x1F]
	}
	return string(b[:])
}

func shortName(s string) string {
	if len(s) > 6 {
		return s[len(s)-6:]
	}
	return strings.TrimPrefix(s, "f-")
}

func pickCommitMessage(rng *rand.Rand) string {
	return commitMessages[rng.IntN(len(commitMessages))]
}

func pickAuthor(rng *rand.Rand) string {
	return authors[rng.IntN(len(authors))]
}

// newPromotion constructs a Pending promotion record for a fresh
// PromoteToStage call.
func newPromotion(project string, stage *kargoapi.Stage, freight *kargoapi.Freight, now time.Time) *kargoapi.Promotion {
	name := fmt.Sprintf("%s.%s.%s", stage.Name, randULIDLike(now), shortName(freight.Name))
	return &kargoapi.Promotion{
		TypeMeta: metav1.TypeMeta{Kind: "Promotion", APIVersion: "kargo.akuity.io/v1alpha1"},
		ObjectMeta: metav1.ObjectMeta{
			Name:              name,
			Namespace:         project,
			CreationTimestamp: metav1.NewTime(now),
		},
		Spec: kargoapi.PromotionSpec{
			Stage:   stage.Name,
			Freight: freight.Name,
		},
		Status: kargoapi.PromotionStatus{Phase: kargoapi.PromotionPhasePending},
	}
}

// --- procedural name banks ---

var aliasAdjectives = []string{
	"blue", "amber", "crimson", "verdant", "lunar", "solar", "silent",
	"swift", "bold", "calm", "bright", "dusky", "frosty", "golden",
	"happy", "lucky", "noble", "rapid", "sunny", "wise",
}

var aliasNouns = []string{
	"otter", "falcon", "comet", "willow", "tiger", "raven", "kestrel",
	"meadow", "summit", "harbor", "canyon", "delta", "ridge", "valley",
	"river", "forest", "ocean", "thicket", "glacier", "horizon",
}

var commitMessages = []string{
	"feat: introduce per-tenant cache",
	"fix: handle empty payload in webhook",
	"chore: bump dependencies",
	"refactor: extract retry helper",
	"docs: clarify rate limits",
	"perf: batch image lookups",
	"test: cover edge case in parser",
	"feat: add Prometheus metrics",
	"fix: race in shutdown path",
	"chore: rotate signing keys",
}

var authors = []string{
	"alice@acme.io", "bob@acme.io", "carol@acme.io", "dave@acme.io",
	"erin@acme.io", "frank@acme.io", "grace@acme.io", "heidi@acme.io",
}
