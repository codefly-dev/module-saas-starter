"""Schema-agnostic settings runtime owned by the SaaS Starter.

Products define or override their Settings protobuf, generate the normal
Python bindings, and build a concrete typed catalog with ``must_string`` /
``must_bool`` / ``must_enum``. The runtime imports no product schema and is
unaware of how settings are persisted; it is copied unchanged into products,
exactly like the Go ``pkg/settings`` and TypeScript ``@codefly/saas-settings``
runtimes.
"""

from .field import (
    Field,
    NilMessageError,
    SettingsError,
    must_bool,
    must_enum,
    must_string,
)
from .json import DEFAULT_MAXIMUM_JSON_BYTES, JSONCodec

__all__ = [
    "DEFAULT_MAXIMUM_JSON_BYTES",
    "Field",
    "JSONCodec",
    "NilMessageError",
    "SettingsError",
    "must_bool",
    "must_enum",
    "must_string",
]
