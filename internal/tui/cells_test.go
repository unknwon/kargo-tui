package tui

import (
	"testing"

	"github.com/stretchr/testify/require"

	"unknwon.dev/kargo-tui/internal/kargo"
)

func TestCurrentStageNames(t *testing.T) {
	names := currentStageNames(
		kargo.Freight{
			CurrentlyIn:    []string{"deploy-a", "control-a"},
			VerifiedStages: []string{"deploy-b", "control-a", "control-b"},
		},
		map[string]bool{
			"control-a": true,
			"control-b": true,
		},
	)

	require.Equal(t, []string{"deploy-a", "control-a", "control-b"}, names)
}
