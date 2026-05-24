package tui

import (
	"testing"

	"github.com/stretchr/testify/require"

	"unknwon.dev/kargo-tui/internal/kargo"
)

func TestCurrentFreightNames(t *testing.T) {
	t.Run("uses current freight", func(t *testing.T) {
		names := currentFreightNames(kargo.Stage{
			CurrentFreight: []string{"freight-a", "freight-b"},
			FreightSummary: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		})

		require.Equal(t, []string{"freight-a", "freight-b"}, names)
	})

	t.Run("falls back to freight summary", func(t *testing.T) {
		names := currentFreightNames(kargo.Stage{
			FreightSummary: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		})

		require.Equal(t, []string{"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}, names)
	})

	t.Run("ignores aggregate summaries", func(t *testing.T) {
		names := currentFreightNames(kargo.Stage{
			FreightSummary: "3/5 Fulfilled",
		})

		require.Empty(t, names)
	})
}

func TestMergeStageNames(t *testing.T) {
	names := mergeStageNames(
		[]string{"deploy-a", "control-a"},
		[]string{"control-a", "control-b"},
	)

	require.Equal(t, []string{"deploy-a", "control-a", "control-b"}, names)
}
