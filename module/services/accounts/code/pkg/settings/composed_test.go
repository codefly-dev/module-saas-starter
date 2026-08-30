package settings_test

import (
	"testing"

	"accounts/pkg/settings"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/descriptorpb"
)

// FileDescriptorProto.options is a singular message with optional leaf fields —
// the same shape a composed settings container has — so it exercises the
// container reflection without importing a product schema.

func TestValidateComposedField(t *testing.T) {
	proto := &descriptorpb.FileDescriptorProto{}
	require.NoError(t, settings.ValidateComposedField(proto, "options", "go_package"))
	require.Error(t, settings.ValidateComposedField(proto, "options", "not_a_field"))
	// name is a singular string, not a message container.
	require.Error(t, settings.ValidateComposedField(proto, "name", "anything"))
	require.Error(t, settings.ValidateComposedField(proto, "no_such_container", "x"))
}

func TestAccessComposedFieldPresenceAndAbsence(t *testing.T) {
	// Absent container reads as not-present and never errors.
	present, err := settings.AccessComposedField(&descriptorpb.FileDescriptorProto{}, "options", "go_package", false)
	require.NoError(t, err)
	require.False(t, present)

	// Nil message is not-present.
	present, err = settings.AccessComposedField((*descriptorpb.FileDescriptorProto)(nil), "options", "go_package", false)
	require.NoError(t, err)
	require.False(t, present)

	withValue := &descriptorpb.FileDescriptorProto{
		Options: &descriptorpb.FileOptions{GoPackage: proto.String("example/pkg")},
	}
	present, err = settings.AccessComposedField(withValue, "options", "go_package", false)
	require.NoError(t, err)
	require.True(t, present)
}

func TestAccessComposedFieldClearPrunesEmptyContainer(t *testing.T) {
	msg := &descriptorpb.FileDescriptorProto{
		Options: &descriptorpb.FileOptions{GoPackage: proto.String("example/pkg")},
	}
	present, err := settings.AccessComposedField(msg, "options", "go_package", true)
	require.NoError(t, err)
	require.True(t, present)
	require.Nil(t, msg.Options, "the last cleared field must prune its empty container")
}

func TestAccessComposedFieldClearPreservesSiblings(t *testing.T) {
	msg := &descriptorpb.FileDescriptorProto{
		Options: &descriptorpb.FileOptions{
			GoPackage:         proto.String("example/pkg"),
			JavaMultipleFiles: proto.Bool(true),
		},
	}
	present, err := settings.AccessComposedField(msg, "options", "go_package", true)
	require.NoError(t, err)
	require.True(t, present)
	require.NotNil(t, msg.Options, "a sibling field must keep the container alive")
	require.Nil(t, msg.Options.GoPackage)
	require.True(t, msg.Options.GetJavaMultipleFiles())
}

func TestAccessComposedFieldMissingFieldErrors(t *testing.T) {
	_, err := settings.AccessComposedField(&descriptorpb.FileDescriptorProto{}, "options", "not_a_field", false)
	require.Error(t, err)
}
