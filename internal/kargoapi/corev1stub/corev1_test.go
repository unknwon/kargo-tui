package corev1stub

import (
	"testing"

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
	if _, err := proto.Marshal(cm); err != nil {
		t.Fatalf("marshal ConfigMap: %v", err)
	}
	se := &Secret{}
	if _, err := proto.Marshal(se); err != nil {
		t.Fatalf("marshal Secret: %v", err)
	}
	for _, name := range []string{
		"k8s.io.api.core.v1.ConfigMap",
		"k8s.io.api.core.v1.Secret",
	} {
		if _, err := protoregistry.GlobalTypes.FindMessageByName(
			protoreflect.FullName(name),
		); err != nil {
			t.Errorf("lookup %s: %v", name, err)
		}
	}
}

