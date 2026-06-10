package tracing

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
)

func TestInit(t *testing.T) {
	t.Run("noop when env unset", func(t *testing.T) {
		t.Setenv(envTraceFile, "")
		shutdown, err := Init("kargo-tui", "test")
		require.NoError(t, err)
		_, span := Start(context.Background(), "should-be-noop")
		// Noop tracer reports IsRecording==false; this is the zero-cost
		// guarantee the package's docstring promises.
		assert.False(t, span.IsRecording())
		span.End()
		require.NoError(t, shutdown(context.Background()))
	})

	t.Run("writes JSON lines when file is set", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "traces.jsonl")
		t.Setenv(envTraceFile, path)
		shutdown, err := Init("kargo-tui", "test")
		require.NoError(t, err)

		_, span := Start(context.Background(), "Update",
			attribute.String("msg.type", "tea.KeyPressMsg"),
			attribute.String("view", "deploys"),
		)
		assert.True(t, span.IsRecording())
		span.End()

		require.NoError(t, shutdown(context.Background()))

		f, err := os.Open(path)
		require.NoError(t, err)
		defer f.Close()

		var lines []jsonlSpan
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			var s jsonlSpan
			require.NoError(t, json.Unmarshal(sc.Bytes(), &s))
			lines = append(lines, s)
		}
		require.NoError(t, sc.Err())
		require.Len(t, lines, 1, "expected exactly one span line in the file")

		got := lines[0]
		assert.Equal(t, "Update", got.Name)
		assert.NotEmpty(t, got.TraceID)
		assert.NotEmpty(t, got.SpanID)
		assert.Empty(t, got.ParentSpanID, "top-level span should have empty parent")
		assert.Equal(t, "tea.KeyPressMsg", got.Attrs["msg.type"])
		assert.Equal(t, "deploys", got.Attrs["view"])
	})
}
