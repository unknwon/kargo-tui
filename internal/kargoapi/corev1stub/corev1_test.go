package corev1stub

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
)

// TestStubLoadsAsV2 is the whole point of this package: prove that the
// hand-rolled file descriptor registers cleanly and the stub types can be
// marshalled by the v2 reflector. If this passes, the v2 protobuf init
// panic that vendored Kargo stubs would hit on *v1.ConfigMap is averted.
func TestStubLoadsAsV2(t *testing.T) {
	cm := &ConfigMap{}
	_, err := proto.Marshal(cm)
	require.NoError(t, err)

	se := &Secret{}
	_, err = proto.Marshal(se)
	require.NoError(t, err)

	for _, name := range []string{
		"k8s.io.api.core.v1.ConfigMap",
		"k8s.io.api.core.v1.Secret",
	} {
		_, err := protoregistry.GlobalTypes.FindMessageByName(
			protoreflect.FullName(name),
		)
		assert.NoError(t, err)
	}
}
