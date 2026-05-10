// Package corev1stub stands in for k8s.io/api/core/v1 in our vendored copy
// of the Kargo service.pb.go. It exists for one reason: the upstream type
// is gogo-proto v1 and triggers a "neither a v1 or v2 Message" panic when
// the v2 protobuf reflector walks Kargo's service descriptor at init.
//
// The TUI never calls the ConfigMap or Secret RPCs, so these messages can
// be empty: the resolver only needs Go types that satisfy
// protoreflect.ProtoMessage. Anything that *does* end up calling these
// RPCs against this package will get back zero values — that's fine
// because nothing in this codebase does.
package corev1stub

import (
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/dynamicpb"
)

// stubFile holds the synthetic file descriptor backing all stub types.
// Built once at init from a FileDescriptorProto so we never have to hand-
// encode raw protobuf wire bytes.
var stubFile protoreflect.FileDescriptor

// Cached message descriptors so ProtoReflect can return them without
// re-walking the file each call.
var (
	configMapDesc protoreflect.MessageDescriptor
	secretDesc    protoreflect.MessageDescriptor
	eventDesc     protoreflect.MessageDescriptor
)

func init() {
	syntax := "proto3"
	pkg := "k8s.io.api.core.v1"
	fdp := &descriptorpb.FileDescriptorProto{
		Name:    proto.String("k8s.io/api/core/v1/generated.proto"),
		Package: proto.String(pkg),
		Syntax:  proto.String(syntax),
		MessageType: []*descriptorpb.DescriptorProto{
			{Name: proto.String("ConfigMap")},
			{Name: proto.String("Secret")},
			{Name: proto.String("Event")},
		},
	}
	fd, err := protodesc.NewFile(fdp, nil)
	if err != nil {
		panic("corev1stub: build descriptor: " + err.Error())
	}
	stubFile = fd
	configMapDesc = fd.Messages().ByName("ConfigMap")
	secretDesc = fd.Messages().ByName("Secret")
	eventDesc = fd.Messages().ByName("Event")

	// Register so anything that looks types up by full name (the v2
	// resolver will, for some lookup paths) can find them.
	if err := protoregistry.GlobalFiles.RegisterFile(fd); err == nil {
		_ = protoregistry.GlobalTypes.RegisterMessage(dynamicpb.NewMessageType(configMapDesc))
		_ = protoregistry.GlobalTypes.RegisterMessage(dynamicpb.NewMessageType(secretDesc))
		_ = protoregistry.GlobalTypes.RegisterMessage(dynamicpb.NewMessageType(eventDesc))
	}
}

// ConfigMap is the v2-compliant stub for k8s.io.api.core.v1.ConfigMap.
type ConfigMap struct{}

func (x *ConfigMap) Reset()                        { *x = ConfigMap{} }
func (x *ConfigMap) String() string                { return "" }
func (*ConfigMap) ProtoMessage()                   {}
func (x *ConfigMap) ProtoReflect() protoreflect.Message {
	return dynamicpb.NewMessage(configMapDesc)
}

// Secret is the v2-compliant stub for k8s.io.api.core.v1.Secret.
type Secret struct{}

func (x *Secret) Reset()                        { *x = Secret{} }
func (x *Secret) String() string                { return "" }
func (*Secret) ProtoMessage()                   {}
func (x *Secret) ProtoReflect() protoreflect.Message {
	return dynamicpb.NewMessage(secretDesc)
}

// Event is the v2-compliant stub for k8s.io.api.core.v1.Event.
type Event struct{}

func (x *Event) Reset()                        { *x = Event{} }
func (x *Event) String() string                { return "" }
func (*Event) ProtoMessage()                   {}
func (x *Event) ProtoReflect() protoreflect.Message {
	return dynamicpb.NewMessage(eventDesc)
}
