package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	tea "charm.land/bubbletea/v2"

	"unknwon.dev/kargo-tui/internal/kargo"
	"unknwon.dev/kargo-tui/internal/tui"
)

func main() {
	namespace := flag.String("namespace", "", "Kargo namespace to list resources from. Leave empty to pick interactively.")
	flag.Parse()

	ctx := context.Background()

	// If no namespace was provided, try to auto-select when there's exactly
	// one Kargo project namespace; otherwise fall through to the picker.
	if *namespace == "" {
		nss, err := kargo.ListProjects(ctx)
		if err == nil && len(nss) == 1 {
			*namespace = nss[0]
		}
	}

	var p *tea.Program
	if *namespace == "" {
		p = tea.NewProgram(tui.NewWithPicker())
	} else {
		deploys, err := kargo.ListStages(ctx, *namespace)
		if err != nil {
			fmt.Fprintln(os.Stderr, "error loading deploys:", err)
			os.Exit(1)
		}
		freights, err := kargo.ListFreight(ctx, *namespace)
		if err != nil {
			fmt.Fprintln(os.Stderr, "error loading freights:", err)
			os.Exit(1)
		}
		p = tea.NewProgram(tui.New(*namespace, deploys, freights))
	}

	if _, err := p.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
