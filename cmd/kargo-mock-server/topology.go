package main

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// topology is the hand-edited shape of one project. The procedural generator
// fills in freight/promotions/events on top of this.
type topology struct {
	Project    string            `yaml:"project"`
	Warehouses []warehouseTopo   `yaml:"warehouses"`
	Stages     []stageTopo       `yaml:"stages"`
	ArgoCDURL  string            `yaml:"argoCDURL,omitempty"`
	Vars       map[string]string `yaml:"vars,omitempty"`
}

type warehouseTopo struct {
	Name string        `yaml:"name"`
	Git  []gitRepoTopo `yaml:"git,omitempty"`
	Img  []imgRepoTopo `yaml:"image,omitempty"`
}

type gitRepoTopo struct {
	RepoURL string `yaml:"repoURL"`
	Branch  string `yaml:"branch,omitempty"`
}

type imgRepoTopo struct {
	RepoURL string `yaml:"repoURL"`
}

type stageTopo struct {
	Name             string   `yaml:"name"`
	Warehouses       []string `yaml:"warehouses,omitempty"`       // direct subscriptions
	Upstreams        []string `yaml:"upstreams,omitempty"`        // upstream stage names
	RequiresApproval bool     `yaml:"requiresApproval,omitempty"` // hotfix-style gate
	Shard            string   `yaml:"shard,omitempty"`
	// ControlFlow marks a stage as a pure routing node — it doesn't
	// incorporate freight itself, only orchestrates promotion downstream.
	// In the generated proto, control-flow stages have no PromotionTemplate
	// so kargoapi.Stage.IsControlFlow() returns true.
	ControlFlow bool `yaml:"controlFlow,omitempty"`
}

// loadTopology reads a single project topology YAML and validates it.
func loadTopology(path string) (*topology, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var t topology
	if err := yaml.Unmarshal(raw, &t); err != nil {
		return nil, fmt.Errorf("parse yaml: %w", err)
	}
	if t.Project == "" {
		return nil, fmt.Errorf("topology missing project name")
	}
	if len(t.Stages) == 0 {
		return nil, fmt.Errorf("topology %q has no stages", t.Project)
	}
	if len(t.Warehouses) == 0 {
		return nil, fmt.Errorf("topology %q has no warehouses", t.Project)
	}

	// Build a name index and validate every reference resolves.
	stageNames := make(map[string]struct{}, len(t.Stages))
	for _, s := range t.Stages {
		if s.Name == "" {
			return nil, fmt.Errorf("topology %q has unnamed stage", t.Project)
		}
		if _, dup := stageNames[s.Name]; dup {
			return nil, fmt.Errorf("topology %q has duplicate stage %q", t.Project, s.Name)
		}
		stageNames[s.Name] = struct{}{}
	}
	warehouseNames := make(map[string]struct{}, len(t.Warehouses))
	for _, w := range t.Warehouses {
		if w.Name == "" {
			return nil, fmt.Errorf("topology %q has unnamed warehouse", t.Project)
		}
		warehouseNames[w.Name] = struct{}{}
	}
	for _, s := range t.Stages {
		for _, u := range s.Upstreams {
			if _, ok := stageNames[u]; !ok {
				return nil, fmt.Errorf("stage %q references unknown upstream %q", s.Name, u)
			}
		}
		for _, w := range s.Warehouses {
			if _, ok := warehouseNames[w]; !ok {
				return nil, fmt.Errorf("stage %q references unknown warehouse %q", s.Name, w)
			}
		}
		if len(s.Upstreams) == 0 && len(s.Warehouses) == 0 {
			return nil, fmt.Errorf("stage %q has neither upstreams nor warehouse subscriptions", s.Name)
		}
	}

	// Cycle check: simple DFS coloring on the stage DAG.
	const (
		white = 0
		gray  = 1
		black = 2
	)
	colors := make(map[string]int, len(t.Stages))
	byName := make(map[string]*stageTopo, len(t.Stages))
	for i := range t.Stages {
		byName[t.Stages[i].Name] = &t.Stages[i]
	}
	var visit func(name string) error
	visit = func(name string) error {
		switch colors[name] {
		case gray:
			return fmt.Errorf("cycle detected at stage %q", name)
		case black:
			return nil
		}
		colors[name] = gray
		for _, u := range byName[name].Upstreams {
			if err := visit(u); err != nil {
				return err
			}
		}
		colors[name] = black
		return nil
	}
	for _, s := range t.Stages {
		if err := visit(s.Name); err != nil {
			return nil, err
		}
	}

	return &t, nil
}

// downstreamsOf returns the names of stages that list `stage` as an upstream.
func (t *topology) downstreamsOf(stage string) []string {
	var out []string
	for _, s := range t.Stages {
		for _, u := range s.Upstreams {
			if u == stage {
				out = append(out, s.Name)
				break
			}
		}
	}
	return out
}
