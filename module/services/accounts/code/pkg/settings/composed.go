package settings

import (
	"fmt"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// ValidateComposedField confirms that prototype declares a singular message
// field named container and that the container message declares a field named
// name. Product catalogs call this once during initialization so a schema or
// code-generation mismatch fails loudly rather than silently dropping a
// settings path at runtime.
func ValidateComposedField(prototype proto.Message, container, name string) error {
	root := prototype.ProtoReflect().Descriptor()
	field := root.Fields().ByName(protoreflect.Name(container))
	if field == nil || field.Message() == nil {
		return fmt.Errorf("generated %s is missing the %q settings container", root.Name(), container)
	}
	if field.Message().Fields().ByName(protoreflect.Name(name)) == nil {
		return fmt.Errorf("generated %s composed settings is missing field %q", root.Name(), name)
	}
	return nil
}

// AccessComposedField reports whether name is explicitly present in message's
// container sub-message. When clear is true and the field is present, it is
// removed and the container itself pruned when no sibling field remains, so
// sparse ProtoJSON never persists a meaningless {"container": {}}.
//
// A nil message, or one whose container is absent, reads as not-present; this is
// the same presence contract Field uses, kept in one place so the user-scoped
// and org-scoped catalogs cannot drift apart.
func AccessComposedField(message proto.Message, container, name string, clear bool) (bool, error) {
	if isNilProto(message) {
		return false, nil
	}
	root := message.ProtoReflect()
	containerField := root.Descriptor().Fields().ByName(protoreflect.Name(container))
	if containerField == nil || containerField.Message() == nil {
		return false, fmt.Errorf("generated %s is missing the %q settings container", root.Descriptor().Name(), container)
	}
	field := containerField.Message().Fields().ByName(protoreflect.Name(name))
	if field == nil {
		return false, fmt.Errorf("generated %s composed settings is missing field %q", root.Descriptor().Name(), name)
	}
	if !root.Has(containerField) {
		return false, nil
	}
	composed := root.Get(containerField).Message()
	present := composed.Has(field)
	if clear && present {
		composed.Clear(field)
		hasSibling := false
		composed.Range(func(protoreflect.FieldDescriptor, protoreflect.Value) bool {
			hasSibling = true
			return false
		})
		if !hasSibling {
			root.Clear(containerField)
		}
	}
	return present, nil
}
