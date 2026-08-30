"""Shared test catalog, exposed as fixtures so the suite carries no
test-to-test imports and stays correct under any pytest import mode.

The schema-agnostic runtime is exercised through a bundled generated proto,
``FileDescriptorProto``, whose nested ``options.features.field_presence`` path
gives real missing/present-but-empty parents and explicit-zero leaves without
generating a throwaway schema — the same vehicle the Go pkg/settings tests use.
"""

import pytest
from google.protobuf import descriptor_pb2 as pb

import saas_settings as settings


@pytest.fixture(scope="session")
def go_package() -> settings.Field:
    return settings.must_string(
        pb.FileDescriptorProto(), "options.go_package", "example/default"
    )


@pytest.fixture(scope="session")
def java_multiple_files() -> settings.Field:
    return settings.must_bool(
        pb.FileDescriptorProto(), "options.java_multiple_files", False
    )


@pytest.fixture(scope="session")
def optimize_for() -> settings.Field:
    return settings.must_enum(
        pb.FileDescriptorProto(), "options.optimize_for", pb.FileOptions.SPEED
    )


@pytest.fixture(scope="session")
def field_presence() -> settings.Field:
    return settings.must_enum(
        pb.FileDescriptorProto(),
        "options.features.field_presence",
        pb.FeatureSet.EXPLICIT,
    )


@pytest.fixture
def make_codec():
    def _make(maximum_bytes: int = 1024) -> settings.JSONCodec:
        return settings.JSONCodec(pb.FileDescriptorProto, maximum_bytes)

    return _make
