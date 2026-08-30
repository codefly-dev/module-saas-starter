"""Field behavior, mirroring the Go pkg/settings field_test.go matrix.

Catalog Fields come from fixtures in conftest.py; enum values are the bundled
proto's own generated constants.
"""

import pytest
from google.protobuf import descriptor_pb2 as pb
from google.protobuf import type_pb2

import saas_settings as settings


def test_nested_set_materializes_missing_parents(go_package):
    message = pb.FileDescriptorProto()
    assert not message.HasField("options")

    go_package.set(message, "example/service")

    assert message.HasField("options")
    assert message.options.HasField("go_package")
    assert message.options.go_package == "example/service"
    value, present = go_package.lookup(message)
    assert present
    assert value == "example/service"


def test_missing_parent_returns_default_without_materializing(go_package):
    message = pb.FileDescriptorProto()

    assert go_package.get(message) == "example/default"
    assert go_package.default == "example/default"
    assert not message.HasField("options"), "a read must never materialize a parent"

    _, present = go_package.lookup(message)
    assert not present


def test_apply_default_materializes_missing_parents(go_package):
    message = pb.FileDescriptorProto()

    changed = go_package.apply_default(message)

    assert changed
    assert message.HasField("options")
    assert message.options.go_package == "example/default"


@pytest.mark.parametrize(
    "make_message",
    [
        lambda: pb.FileDescriptorProto(),
        lambda: pb.FileDescriptorProto(options=pb.FileOptions()),
        lambda: pb.FileDescriptorProto(options=pb.FileOptions(features=pb.FeatureSet())),
    ],
    ids=["all parents absent", "outer parent present", "all parents present"],
)
def test_apply_default_across_nested_parent_states_is_idempotent(
    make_message, field_presence
):
    message = make_message()

    changed = field_presence.apply_default(message)
    assert changed
    assert message.options.features.field_presence == pb.FeatureSet.EXPLICIT

    changed = field_presence.apply_default(message)
    assert not changed, "applying a default twice must be a no-op"


def test_apply_default_never_overwrites_explicit_zero_value(java_multiple_files):
    message = pb.FileDescriptorProto()
    java_multiple_files.set(message, False)

    changed = java_multiple_files.apply_default(message)

    assert not changed
    value, present = java_multiple_files.lookup(message)
    assert present
    assert value is False


def test_get_never_coalesces_explicit_scalar_zero_values(
    go_package, java_multiple_files, field_presence
):
    message = pb.FileDescriptorProto()
    go_package.set(message, "")
    java_multiple_files.set(message, False)
    field_presence.set(message, pb.FeatureSet.FIELD_PRESENCE_UNKNOWN)

    text, present = go_package.lookup(message)
    assert present
    assert text == ""
    assert go_package.get(message) == "", (
        "explicit empty string must not resolve to its non-empty default"
    )

    flag, present = java_multiple_files.lookup(message)
    assert present
    assert flag is False

    enum, present = field_presence.lookup(message)
    assert present
    assert enum == pb.FeatureSet.FIELD_PRESENCE_UNKNOWN
    assert field_presence.get(message) == pb.FeatureSet.FIELD_PRESENCE_UNKNOWN, (
        "explicit zero enum must not resolve to its non-zero default"
    )


def test_set_and_clear_materialize_and_prune_multiple_missing_parents(field_presence):
    message = pb.FileDescriptorProto()

    field_presence.set(message, pb.FeatureSet.IMPLICIT)
    assert message.options.features.field_presence == pb.FeatureSet.IMPLICIT

    field_presence.clear(message)
    assert not message.HasField("options"), "all empty parents in the path must be pruned"


def test_explicit_scalar_default_preserves_presence(java_multiple_files):
    message = pb.FileDescriptorProto()

    java_multiple_files.set(message, False)

    value, present = java_multiple_files.lookup(message)
    assert present, "explicit false must not collapse into unset"
    assert value is False


def test_sibling_set_survives_clear_and_final_clear_prunes_parent(
    go_package, java_multiple_files
):
    message = pb.FileDescriptorProto()
    go_package.set(message, "example/service")
    java_multiple_files.set(message, True)

    go_package.clear(message)
    assert message.HasField("options"), "parent still contains a sibling setting"
    assert message.options.java_multiple_files is True

    java_multiple_files.clear(message)
    assert not message.HasField("options"), "empty parent must be pruned"


def test_clear_missing_nested_path_is_a_no_op(go_package):
    message = pb.FileDescriptorProto()
    go_package.clear(message)
    assert not message.HasField("options")


def test_clear_is_idempotent_across_partially_materialized_parents(field_presence):
    message = pb.FileDescriptorProto(
        options=pb.FileOptions(features=pb.FeatureSet())
    )

    field_presence.clear(message)
    assert not message.HasField("options")
    field_presence.clear(message)
    assert not message.HasField("options")


def test_clear_does_not_prune_a_parent_containing_unknown_wire_fields(go_package):
    message = pb.FileDescriptorProto(options=pb.FileOptions())
    # field 100, varint 1 — unknown to FileOptions and preserved on the wire.
    message.options.MergeFromString(bytes([0xA0, 0x06, 0x01]))
    go_package.set(message, "example/service")

    go_package.clear(message)

    assert message.HasField("options")
    assert message.options.SerializeToString() == bytes([0xA0, 0x06, 0x01])


def test_enum_set_rejects_undefined_values(optimize_for):
    message = pb.FileDescriptorProto()
    with pytest.raises(settings.SettingsError, match="not defined"):
        optimize_for.set(message, 999)
    assert not message.HasField("options")


def test_enum_set_and_default(optimize_for):
    message = pb.FileDescriptorProto()
    assert optimize_for.get(message) == pb.FileOptions.SPEED

    optimize_for.set(message, pb.FileOptions.CODE_SIZE)
    value, present = optimize_for.lookup(message)
    assert present
    assert value == pb.FileOptions.CODE_SIZE


def test_failed_nested_enum_set_rolls_back_only_new_parents(field_presence):
    message = pb.FileDescriptorProto(options=pb.FileOptions(java_package="com.example"))

    with pytest.raises(settings.SettingsError, match="not defined"):
        field_presence.set(message, 999)

    assert message.HasField("options"), "pre-existing outer parent must survive"
    assert message.options.java_package == "com.example"
    assert not message.options.HasField("features"), "new inner parent must not materialize"


def test_nil_messages_fail_without_raising_attribute_errors(go_package):
    for operation in (
        lambda: go_package.get(None),
        lambda: go_package.lookup(None),
        lambda: go_package.has(None),
        lambda: go_package.apply_default(None),
        lambda: go_package.set(None, "value"),
        lambda: go_package.clear(None),
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


def test_field_belonging_to_another_message_is_rejected(go_package):
    with pytest.raises(settings.SettingsError, match="belongs to"):
        go_package.get(pb.FileOptions())
