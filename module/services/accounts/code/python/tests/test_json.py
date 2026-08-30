"""ProtoJSON codec behavior, mirroring the Go pkg/settings json_test.go matrix."""

import json

import pytest
from google.protobuf import descriptor_pb2 as pb

import saas_settings as settings


def test_round_trips_nested_presence_using_proto_names(
    make_codec, go_package, java_multiple_files
):
    message = pb.FileDescriptorProto()
    go_package.set(message, "example/service")
    java_multiple_files.set(message, False)

    encoded = make_codec().marshal(message)
    assert json.loads(encoded) == {
        "options": {"go_package": "example/service", "java_multiple_files": False}
    }

    decoded = make_codec().unmarshal(encoded)
    value, present = java_multiple_files.lookup(decoded)
    assert present
    assert value is False


def test_empty_storage_is_an_empty_message(make_codec):
    for encoded in (None, b"", "", b"{}", "{}"):
        decoded = make_codec().unmarshal(encoded)
        assert not decoded.HasField("options")


def test_ignores_removed_unknown_fields_on_read(make_codec):
    decoded = make_codec().unmarshal('{"name":"settings.proto","removed_setting":true}')
    assert decoded.name == "settings.proto"


def test_treats_null_as_absent_rather_than_a_third_scalar_state(make_codec, go_package):
    decoded = make_codec().unmarshal('{"options":{"go_package":null}}')

    value, present = go_package.lookup(decoded)
    assert not present
    assert value is None
    assert go_package.get(decoded) == "example/default"


@pytest.mark.parametrize(
    "encoded",
    [
        '{"options":null}',
        '{"options":{"features":null}}',
        '{"options":{"features":{"field_presence":null}}}',
    ],
    ids=["outer parent null", "inner parent null", "leaf null"],
)
def test_treats_null_at_every_nested_level_as_absence(make_codec, field_presence, encoded):
    decoded = make_codec().unmarshal(encoded)
    _, present = field_presence.lookup(decoded)
    assert not present
    assert field_presence.get(decoded) == pb.FeatureSet.EXPLICIT


def test_preserves_explicit_scalar_zero_presence(
    make_codec, go_package, java_multiple_files, field_presence
):
    decoded = make_codec().unmarshal(
        '{"options":{"go_package":"","java_multiple_files":false,'
        '"features":{"field_presence":"FIELD_PRESENCE_UNKNOWN"}}}'
    )

    text, present = go_package.lookup(decoded)
    assert present
    assert text == ""
    flag, present = java_multiple_files.lookup(decoded)
    assert present
    assert flag is False
    enum, present = field_presence.lookup(decoded)
    assert present
    assert enum == pb.FeatureSet.FIELD_PRESENCE_UNKNOWN


def test_empty_object_parent_is_present_but_leaf_remains_absent(make_codec, go_package):
    decoded = make_codec().unmarshal('{"options":{}}')
    assert decoded.HasField("options")

    _, present = go_package.lookup(decoded)
    assert not present
    assert go_package.get(decoded) == "example/default"
    assert decoded.HasField("options"), "get must not rewrite or prune the document"


def test_rejects_malformed_and_type_invalid_json(make_codec):
    with pytest.raises(settings.SettingsError, match="unmarshal protobuf settings JSON"):
        make_codec().unmarshal('{"options":')
    with pytest.raises(settings.SettingsError, match="unmarshal protobuf settings JSON"):
        make_codec().unmarshal('{"options":{"go_package":false}}')


def test_rejects_invalid_utf8_as_a_settings_error(make_codec):
    with pytest.raises(settings.SettingsError, match="unmarshal protobuf settings JSON"):
        make_codec().unmarshal(b"\xff\xfe not utf-8")


def test_rejects_oversized_reads_and_writes(make_codec):
    small = make_codec(8)
    with pytest.raises(settings.SettingsError, match="maximum is 8"):
        small.unmarshal('{"name":"too-large"}')

    message = pb.FileDescriptorProto(name="too-large")
    with pytest.raises(settings.SettingsError, match="maximum is 8"):
        small.marshal(message)


def test_rejects_invalid_factories_and_nil_messages(make_codec):
    with pytest.raises(settings.SettingsError):
        settings.JSONCodec(None, 1024)
    with pytest.raises(settings.SettingsError):
        settings.JSONCodec(lambda: None, 1024)

    with pytest.raises(settings.NilMessageError):
        make_codec().marshal(None)
