"""ProtoJSON codec behavior, mirroring the Go pkg/settings json_test.go matrix."""

import json

import pytest
from google.protobuf import descriptor_pb2 as pb

import saas_settings as settings
from test_field import (
    FIELD_PRESENCE,
    FIELD_PRESENCE_EXPLICIT,
    FIELD_PRESENCE_UNKNOWN,
    GO_PACKAGE,
    JAVA_MULTIPLE_FILES,
)


def codec(maximum_bytes: int = 1024) -> settings.JSONCodec:
    return settings.JSONCodec(pb.FileDescriptorProto, maximum_bytes)


def test_round_trips_nested_presence_using_proto_names():
    message = pb.FileDescriptorProto()
    GO_PACKAGE.set(message, "example/service")
    JAVA_MULTIPLE_FILES.set(message, False)

    encoded = codec().marshal(message)
    assert json.loads(encoded) == {
        "options": {"go_package": "example/service", "java_multiple_files": False}
    }

    decoded = codec().unmarshal(encoded)
    value, present = JAVA_MULTIPLE_FILES.lookup(decoded)
    assert present
    assert value is False


def test_empty_storage_is_an_empty_message():
    for encoded in (None, b"", "", b"{}", "{}"):
        decoded = codec().unmarshal(encoded)
        assert not decoded.HasField("options")


def test_ignores_removed_unknown_fields_on_read():
    decoded = codec().unmarshal('{"name":"settings.proto","removed_setting":true}')
    assert decoded.name == "settings.proto"


def test_treats_null_as_absent_rather_than_a_third_scalar_state():
    decoded = codec().unmarshal('{"options":{"go_package":null}}')

    value, present = GO_PACKAGE.lookup(decoded)
    assert not present
    assert value is None
    assert GO_PACKAGE.get(decoded) == "example/default"


@pytest.mark.parametrize(
    "encoded",
    [
        '{"options":null}',
        '{"options":{"features":null}}',
        '{"options":{"features":{"field_presence":null}}}',
    ],
    ids=["outer parent null", "inner parent null", "leaf null"],
)
def test_treats_null_at_every_nested_level_as_absence(encoded):
    decoded = codec().unmarshal(encoded)
    _, present = FIELD_PRESENCE.lookup(decoded)
    assert not present
    assert FIELD_PRESENCE.get(decoded) == FIELD_PRESENCE_EXPLICIT


def test_preserves_explicit_scalar_zero_presence():
    decoded = codec().unmarshal(
        '{"options":{"go_package":"","java_multiple_files":false,'
        '"features":{"field_presence":"FIELD_PRESENCE_UNKNOWN"}}}'
    )

    text, present = GO_PACKAGE.lookup(decoded)
    assert present
    assert text == ""
    flag, present = JAVA_MULTIPLE_FILES.lookup(decoded)
    assert present
    assert flag is False
    enum, present = FIELD_PRESENCE.lookup(decoded)
    assert present
    assert enum == FIELD_PRESENCE_UNKNOWN


def test_empty_object_parent_is_present_but_leaf_remains_absent():
    decoded = codec().unmarshal('{"options":{}}')
    assert decoded.HasField("options")

    _, present = GO_PACKAGE.lookup(decoded)
    assert not present
    assert GO_PACKAGE.get(decoded) == "example/default"
    assert decoded.HasField("options"), "get must not rewrite or prune the document"


def test_rejects_malformed_and_type_invalid_json():
    with pytest.raises(settings.SettingsError, match="unmarshal protobuf settings JSON"):
        codec().unmarshal('{"options":')
    with pytest.raises(settings.SettingsError, match="unmarshal protobuf settings JSON"):
        codec().unmarshal('{"options":{"go_package":false}}')


def test_rejects_oversized_reads_and_writes():
    small = codec(8)
    with pytest.raises(settings.SettingsError, match="maximum is 8"):
        small.unmarshal('{"name":"too-large"}')

    message = pb.FileDescriptorProto(name="too-large")
    with pytest.raises(settings.SettingsError, match="maximum is 8"):
        small.marshal(message)


def test_rejects_invalid_factories_and_nil_messages():
    with pytest.raises(settings.SettingsError):
        settings.JSONCodec(None, 1024)
    with pytest.raises(settings.SettingsError):
        settings.JSONCodec(lambda: None, 1024)

    with pytest.raises(settings.NilMessageError):
        codec().marshal(None)
