// Package kargo wraps the Kubernetes client to read Kargo CRDs (Stages,
// Freight, Promotions, Projects) and supporting cluster resources, returning
// flattened, UI-friendly types tailored for the TUI.
package kargo

import (
	"fmt"
	"sort"

	kargoapi "github.com/akuity/kargo/api/v1alpha1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// newClient builds a controller-runtime client from the user's kubeconfig
// with the schemes the TUI needs registered (core, kargo, networking).
func newClient() (client.Client, error) {
	cfg, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		clientcmd.NewDefaultClientConfigLoadingRules(),
		&clientcmd.ConfigOverrides{},
	).ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("load kubeconfig: %w", err)
	}

	sc := runtime.NewScheme()
	if err := scheme.AddToScheme(sc); err != nil {
		return nil, fmt.Errorf("register core scheme: %w", err)
	}
	if err := kargoapi.AddToScheme(sc); err != nil {
		return nil, fmt.Errorf("register kargo scheme: %w", err)
	}
	if err := networkingv1.AddToScheme(sc); err != nil {
		return nil, fmt.Errorf("register networking scheme: %w", err)
	}

	c, err := client.New(cfg, client.Options{Scheme: sc})
	if err != nil {
		return nil, fmt.Errorf("build client: %w", err)
	}
	return c, nil
}

// mapKeys returns the sorted keys of a string-keyed map.
func mapKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// pickString returns the first non-empty string field at the given keys, or
// "" if none are present. Used to tolerate the inconsistent casing in
// Kargo's health-output JSON.
func pickString(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if v, ok := m[k].(string); ok && v != "" {
			return v
		}
	}
	return ""
}
