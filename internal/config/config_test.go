package config

import (
	"path/filepath"
	"testing"
)

func TestPath(t *testing.T) {
	t.Run("relative config override returns absolute path", func(t *testing.T) {
		rel := filepath.Join("relative", "config.yaml")
		expected, err := filepath.Abs(rel)
		if err != nil {
			t.Fatalf("resolve expected path: %v", err)
		}

		t.Setenv("KARGO_TUI_CONFIG", rel)

		got, err := Path()
		if err != nil {
			t.Fatalf("path: %v", err)
		}
		if got != expected {
			t.Fatalf("expected %q, got %q", expected, got)
		}
	})
}
