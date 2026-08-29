"""The single conversion boundary between typed protobuf settings and their
JSONB representation.

Proto field names keep persisted keys stable across Go, TypeScript, and
Python; unknown fields are discarded on read so schema removals roll safely
without deleting older keys from the database.
"""

from __future__ import annotations

import json
from typing import Callable, Generic, TypeVar

from google.protobuf import json_format
from google.protobuf.message import Message

from .field import NilMessageError, SettingsError

M = TypeVar("M", bound=Message)

DEFAULT_MAXIMUM_JSON_BYTES = 128 * 1024


class JSONCodec(Generic[M]):
    def __init__(
        self,
        new_message: Callable[[], M],
        maximum_bytes: int = DEFAULT_MAXIMUM_JSON_BYTES,
    ) -> None:
        if new_message is None:
            raise SettingsError("settings message factory is required")
        if maximum_bytes <= 0:
            maximum_bytes = DEFAULT_MAXIMUM_JSON_BYTES
        if new_message() is None:
            raise SettingsError("settings message factory returned nil")
        self._new_message = new_message
        self._maximum = maximum_bytes

    def marshal(self, message: M | None) -> bytes:
        if message is None:
            raise NilMessageError("settings message is nil")
        encoded = json.dumps(
            json_format.MessageToDict(message, preserving_proto_field_name=True),
            separators=(",", ":"),
            ensure_ascii=False,
        ).encode("utf-8")
        if len(encoded) > self._maximum:
            raise SettingsError(
                f"protobuf settings JSON is {len(encoded)} bytes; maximum is {self._maximum}"
            )
        return encoded

    def unmarshal(self, encoded: bytes | str | None) -> M:
        raw = b"" if encoded is None else (
            encoded.encode("utf-8") if isinstance(encoded, str) else encoded
        )
        if len(raw) > self._maximum:
            raise SettingsError(
                f"protobuf settings JSON is {len(raw)} bytes; maximum is {self._maximum}"
            )
        message = self._new_message()
        text = raw.decode("utf-8") if raw else "{}"
        try:
            json_format.Parse(text, message, ignore_unknown_fields=True)
        except json_format.ParseError as error:
            raise SettingsError(
                f"unmarshal protobuf settings JSON: {error}"
            ) from error
        return message
