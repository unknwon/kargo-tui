package config

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPath(t *testing.T) {
	t.Run("relative config override returns absolute path", func(t *testing.T) {
		rel := filepath.Join("relative", "config.yaml")
		expected, err := filepath.Abs(rel)
		require.NoError(t, err)

		t.Setenv("KARGO_TUI_CONFIG", rel)

		got, err := Path()
		require.NoError(t, err)
		require.Equal(t, expected, got)
	})
}
