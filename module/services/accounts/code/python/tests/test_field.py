"""Field behavior, mirroring the Go pkg/settings field_test.go matrix.

The schema-agnostic runtime is exercised through a bundled generated proto,
``FileDescriptorProto``, whose nested ``options.features.field_presence`` path
gives real missing/present-but-empty parents and explicit-zero leaves without
generating a throwaway schema.
"""

import pytest
from google.protobuf import descriptor_pb2 as pb
from google.protobuf import type_pb2

import saas_settings as settings

# descriptor.proto enum numbers used as typed defaults.
OPTIMIZE_SPEED = 1
OPTIMIZE_CODE_SIZE = 2
FIELD_PRESENCE_UNKNOWN = 0
FIELD_PRESENCE_EXPLICIT = 1
FIELD_PRESENCE_IMPLICIT = 2

GO_PACKAGE = settings.must_string(
    pb.FileDescriptorProto(), "options.go_package", "example/default"
)
JAVA_MULTIPLE_FILES = settings.must_bool(
    pb.FileDescriptorProto(), "options.java_multiple_files", False
)
OPTIMIZE_FOR = settings.must_enum(
    pb.FileDescriptorProto(), "options.optimize_for", OPTIMIZE_SPEED
)
FIELD_PRESENCE = settings.must_enum(
    pb.FileDescriptorProto(),
    "options.features.field_presence",
    FIELD_PRESENCE_EXPLICIT,
)


def test_nested_set_materializes_missing_parents():
    message = pb.FileDescriptorProto()
    assert not message.HasField("options")

    GO_PACKAGE.set(message, "example/service")

    assert message.HasField("options")
    assert message.options.HasField("go_package")
    assert message.options.go_package == "example/service"
    value, present = GO_PACKAGE.lookup(message)
    assert present
    assert value == "example/service"


def test_missing_parent_returns_default_without_materializing():
    message = pb.FileDescriptorProto()

    assert GO_PACKAGE.get(message) == "example/default"
    assert GO_PACKAGE.default == "example/default"
    assert not message.HasField("options"), "a read must never materialize a parent"

    _, present = GO_PACKAGE.lookup(message)
    assert not present


def test_apply_default_materializes_missing_parents():
    message = pb.FileDescriptorProto()

    changed = GO_PACKAGE.apply_default(message)

    assert changed
    assert message.HasField("options")
    assert message.options.go_package == "example/default"


@pytest.mark.parametrize(
    "message",
    [
        pb.FileDescriptorProto(),
        pb.FileDescriptorProto(options=pb.FileOptions()),
        pb.FileDescriptorProto(options=pb.FileOptions(features=pb.FeatureSet())),
    ],
    ids=["all parents absent", "outer parent present", "all parents present"],
)
def test_apply_default_across_nested_parent_states_is_idempotent(message):
    changed = FIELD_PRESENCE.apply_default(message)
    assert changed
    assert message.options.features.field_presence == FIELD_PRESENCE_EXPLICIT

    changed = FIELD_PRESENCE.apply_default(message)
    assert not changed, "applying a default twice must be a no-op"


def test_apply_default_never_overwrites_explicit_zero_value():
    message = pb.FileDescriptorProto()
    JAVA_MULTIPLE_FILES.set(message, False)

    changed = JAVA_MULTIPLE_FILES.apply_default(message)

    assert not changed
    value, present = JAVA_MULTIPLE_FILES.lookup(message)
    assert present
    assert value is False


def test_get_never_coalesces_explicit_scalar_zero_values():
    message = pb.FileDescriptorProto()
    GO_PACKAGE.set(message, "")
    JAVA_MULTIPLE_FILES.set(message, False)
    FIELD_PRESENCE.set(message, FIELD_PRESENCE_UNKNOWN)

    text, present = GO_PACKAGE.lookup(message)
    assert present
    assert text == ""
    assert GO_PACKAGE.get(message) == "", (
        "explicit empty string must not resolve to its non-empty default"
    )

    flag, present = JAVA_MULTIPLE_FILES.lookup(message)
    assert present
    assert flag is False

    enum, present = FIELD_PRESENCE.lookup(message)
    assert present
    assert enum == FIELD_PRESENCE_UNKNOWN
    assert FIELD_PRESENCE.get(message) == FIELD_PRESENCE_UNKNOWN, (
        "explicit zero enum must not resolve to its non-zero default"
    )


def test_set_and_clear_materialize_and_prune_multiple_missing_parents():
    message = pb.FileDescriptorProto()

    FIELD_PRESENCE.set(message, FIELD_PRESENCE_IMPLICIT)
    assert message.options.features.field_presence == FIELD_PRESENCE_IMPLICIT

    FIELD_PRESENCE.clear(message)
    assert not message.HasField("options"), "all empty parents in the path must be pruned"


def test_explicit_scalar_default_preserves_presence():
    message = pb.FileDescriptorProto()

    JAVA_MULTIPLE_FILES.set(message, False)

    value, present = JAVA_MULTIPLE_FILES.lookup(message)
    assert present, "explicit false must not collapse into unset"
    assert value is False


def test_sibling_set_survives_clear_and_final_clear_prunes_parent():
    message = pb.FileDescriptorProto()
    GO_PACKAGE.set(message, "example/service")
    JAVA_MULTIPLE_FILES.set(message, True)

    GO_PACKAGE.clear(message)
    assert message.HasField("options"), "parent still contains a sibling setting"
    assert message.options.java_multiple_files is True

    JAVA_MULTIPLE_FILES.clear(message)
    assert not message.HasField("options"), "empty parent must be pruned"


def test_clear_missing_nested_path_is_a_no_op():
    message = pb.FileDescriptorProto()
    GO_PACKAGE.clear(message)
    assert not message.HasField("options")


def test_clear_is_idempotent_across_partially_materialized_parents():
    message = pb.FileDescriptorProto(
        options=pb.FileOptions(features=pb.FeatureSet())
    )

    FIELD_PRESENCE.clear(message)
    assert not message.HasField("options")
    FIELD_PRESENCE.clear(message)
    assert not message.HasField("options")


def test_clear_does_not_prune_a_parent_containing_unknown_wire_fields():
    message = pb.FileDescriptorProto(options=pb.FileOptions())
    # field 100, varint 1 — unknown to FileOptions and preserved on the wire.
    message.options.MergeFromString(bytes([0xA0, 0x06, 0x01]))
    GO_PACKAGE.set(message, "example/service")

    GO_PACKAGE.clear(message)

    assert message.HasField("options")
    assert message.options.SerializeToString() == bytes([0xA0, 0x06, 0x01])


def test_enum_set_rejects_undefined_values():
    message = pb.FileDescriptorProto()
    with pytest.raises(settings.SettingsError, match="not defined"):
        OPTIMIZE_FOR.set(message, 999)
    assert not message.HasField("options")


def test_enum_set_and_default():
    message = pb.FileDescriptorProto()
    assert OPTIMIZE_FOR.get(message) == OPTIMIZE_SPEED

    OPTIMIZE_FOR.set(message, OPTIMIZE_CODE_SIZE)
    value, present = OPTIMIZE_FOR.lookup(message)
    assert present
    assert value == OPTIMIZE_CODE_SIZE


def test_failed_nested_enum_set_rolls_back_only_new_parents():
    message = pb.FileDescriptorProto(options=pb.FileOptions(java_package="com.example"))

    with pytest.raises(settings.SettingsError, match="not defined"):
        FIELD_PRESENCE.set(message, 999)

    assert message.HasField("options"), "pre-existing outer parent must survive"
    assert message.options.java_package == "com.example"
    assert not message.options.HasField("features"), "new inner parent must not materialize"


def test_nil_messages_fail_without_raising_attribute_errors():
    for operation in (
        lambda: GO_PACKAGE.get(None),
        lambda: GO_PACKAGE.lookup(None),
        lambda: GO_PACKAGE.has(None),
        lambda: GO_PACKAGE.apply_default(None),
        lambda: GO_PACKAGE.set(None, "value"),
        lambda: GO_PACKAGE.clear(None),
    ):
        with pytest.raises(settings.NilMessageError):
            operation()


def test_invalid_catalog_paths_fail_at_construction():
    with pytest.raises(settings.SettingsError):
        settings.must_string(pb.FileDescriptorProto(), "options.missing", "")
    with pytest.raises(settings.SettingsError):
        settings.must_string(pb.FileDescriptorProto(), "name.child", "")
    with pytest.raises(settings.SettingsError):
        # Type.name is a proto3 scalar without presence.
        settings.must_string(type_pb2.Type(), "name", "")
    with pytest.raises(settings.SettingsError):
        settings.must_enum(pb.FileDescriptorProto(), "options.optimize_for", 999)


def test_field_belonging_to_another_message_is_rejected():
    with pytest.raises(settings.SettingsError, match="belongs to"):
        GO_PACKAGE.get(pb.FileOptions())
