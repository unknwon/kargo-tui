package svcv1alpha1

import (
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

// TestNoInitPanic is the entire reason this package exists. It used to
// panic at import time with "message *v1.ConfigMap is neither a v1 or v2
// Message"; the corev1stub rewrite is supposed to fix that. If this test
// runs to completion, the fix held.
func TestNoInitPanic(t *testing.T) {
	req := &ListStagesRequest{Project: "test-project"}
	b, err := proto.Marshal(req)
	require.NoError(t, err)
	require.NotEmpty(t, b)
}
