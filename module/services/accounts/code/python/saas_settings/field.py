"""Typed, presence-aware access to protobuf-backed settings.

Product code works exclusively with generated protobuf messages and typed
``Field`` values. JSON is an implementation detail of the persistence boundary.

This module is schema-agnostic: it imports no generated schema and is copied
unchanged into products, exactly like the Go ``pkg/settings`` and TypeScript
``@codefly/saas-settings`` runtimes.
"""

from __future__ import annotations

from typing import Callable, Generic, TypeVar

from google.protobuf.descriptor import FieldDescriptor
from google.protobuf.message import Message

T = TypeVar("T")


class SettingsError(Exception):
    """A settings catalog or access error."""


class NilMessageError(SettingsError):
    """Raised when a settings operation is handed no message."""


def _require_message(message: Message | None, path: str, root: str) -> Message:
    if message is None:
        raise NilMessageError("settings message is nil")
    actual = message.DESCRIPTOR.full_name
    if actual != root:
        raise SettingsError(
            f'settings field "{path}" belongs to {root}, got {actual}'
        )
    return message


def _message_empty(message: Message) -> bool:
    # ByteSize covers both explicitly set fields and retained unknown wire
    # fields, so a parent carrying an unrecognized protobuf field is never
    # mistaken for empty and pruned.
    return message.ByteSize() == 0


class Field(Generic[T]):
    """A typed path from a generated protobuf settings message to a scalar T.

    Paths are resolved and validated once when the field catalog is built.
    ``set`` materializes missing parent messages automatically.
    """

    def __init__(
        self,
        *,
        root: str,
        path: str,
        segments: list[FieldDescriptor],
        default: T,
        decode: Callable[[Message, FieldDescriptor], T],
        assign: Callable[[Message, FieldDescriptor, T], None],
    ) -> None:
        self._root = root
        self._path = path
        self._segments = segments
        self._default = default
        self._decode = decode
        self._assign = assign

    @property
    def path(self) -> str:
        """The stable protobuf field path, using proto field names."""
        return self._path

    @property
    def default(self) -> T:
        """The field's configured fallback value."""
        return self._default

    def lookup(self, message: Message | None) -> tuple[T | None, bool]:
        """Return the stored value and its protobuf presence.

        An explicit scalar zero (``False``, ``""``, ``0``, or the zero enum)
        stays distinguishable from an unset field when the schema declares
        presence, as settings protos should.
        """
        current = _require_message(message, self._path, self._root)
        for segment in self._segments[:-1]:
            if not current.HasField(segment.name):
                return None, False
            current = getattr(current, segment.name)
        leaf = self._segments[-1]
        if not current.HasField(leaf.name):
            return None, False
        return self._decode(current, leaf), True

    def has(self, message: Message | None) -> bool:
        """Report whether the complete path is explicitly present."""
        return self.lookup(message)[1]

    def get(self, message: Message | None) -> T:
        """Return the explicit value, or the configured default when the field
        or any of its parent messages is absent."""
        value, present = self.lookup(message)
        if not present:
            return self._default
        return value  # type: ignore[return-value]

    def apply_default(self, message: Message | None) -> bool:
        """Write the configured default only when the field is absent.

        Returns ``True`` when the message changed. Existing values, including
        explicit ``False``, empty string, zero, or the zero enum, are never
        overwritten.
        """
        if self.lookup(message)[1]:
            return False
        self.set(message, self._default)
        return True

    def set(self, message: Message | None, value: T) -> None:
        """Write a typed value and materialize every missing parent message."""
        current = _require_message(message, self._path, self._root)
        for segment in self._segments[:-1]:
            current = getattr(current, segment.name)
        # assign validates before mutating, so a rejected value never
        # materializes a parent along the path.
        self._assign(current, self._segments[-1], value)

    def clear(self, message: Message | None) -> None:
        """Remove a value, pruning parent messages created solely for the
        cleared path so ProtoJSON does not persist meaningless ``{"parent": {}}``."""
        current = _require_message(message, self._path, self._root)
        parents: list[tuple[Message, str]] = []
        for segment in self._segments[:-1]:
            if not current.HasField(segment.name):
                return
            parents.append((current, segment.name))
            current = getattr(current, segment.name)
        current.ClearField(self._segments[-1].name)
        for parent, name in reversed(parents):
            if not _message_empty(getattr(parent, name)):
                break
            parent.ClearField(name)


def _new_field(
    prototype: Message,
    path: str,
    default: T,
    validate: Callable[[FieldDescriptor], None],
    decode: Callable[[Message, FieldDescriptor], T],
    assign: Callable[[Message, FieldDescriptor, T], None],
) -> Field[T]:
    if prototype is None:
        raise SettingsError("settings field prototype is nil")
    path = path.strip()
    if not path:
        raise SettingsError("settings field path is empty")
    descriptor = prototype.DESCRIPTOR
    segments: list[FieldDescriptor] = []
    names = path.split(".")
    for index, name in enumerate(names):
        if name == "":
            raise SettingsError(
                f'settings field path "{path}" contains an empty segment'
            )
        field = descriptor.fields_by_name.get(name)
        if field is None:
            raise SettingsError(
                f'settings field path "{path}": {descriptor.full_name} has no field "{name}"'
            )
        segments.append(field)
        if index < len(names) - 1:
            if field.is_repeated or field.message_type is None:
                raise SettingsError(
                    f'settings field path "{path}": parent "{name}" is not a singular message'
                )
            descriptor = field.message_type
    leaf = segments[-1]
    if leaf.is_repeated:
        raise SettingsError(
            f'settings field path "{path}": list and map leaves are not supported'
        )
    validate(leaf)
    return Field(
        root=prototype.DESCRIPTOR.full_name,
        path=path,
        segments=segments,
        default=default,
        decode=decode,
        assign=assign,
    )


def _require_presence(descriptor: FieldDescriptor) -> None:
    if not descriptor.has_presence:
        raise SettingsError(
            f"field {descriptor.full_name} has no presence; declare settings scalars optional"
        )


def _scalar_field(
    prototype: Message,
    path: str,
    default: T,
    cpp_type: int,
    type_name: str,
) -> Field[T]:
    def validate(descriptor: FieldDescriptor) -> None:
        if descriptor.cpp_type != cpp_type:
            raise SettingsError(f"expected {type_name}")
        _require_presence(descriptor)

    def decode(message: Message, descriptor: FieldDescriptor) -> T:
        return getattr(message, descriptor.name)

    def assign(message: Message, descriptor: FieldDescriptor, value: T) -> None:
        setattr(message, descriptor.name, value)

    return _new_field(prototype, path, default, validate, decode, assign)


def must_string(prototype: Message, path: str, default: str) -> Field[str]:
    """Define an optional string settings field, raising when the path is
    invalid. Catalogs should be module-level values so schema mistakes fail at
    import time rather than on a request."""
    return _scalar_field(
        prototype, path, default, FieldDescriptor.CPPTYPE_STRING, "string"
    )


def must_bool(prototype: Message, path: str, default: bool) -> Field[bool]:
    """Define an optional bool settings field."""
    return _scalar_field(
        prototype, path, default, FieldDescriptor.CPPTYPE_BOOL, "bool"
    )


def must_enum(prototype: Message, path: str, default: int) -> Field[int]:
    """Define an optional enum settings field. The value is the generated
    enum's integer. ``set`` rejects values unknown to the field's enum."""

    def validate(descriptor: FieldDescriptor) -> None:
        if descriptor.cpp_type != FieldDescriptor.CPPTYPE_ENUM:
            raise SettingsError("expected enum")
        _require_presence(descriptor)
        if default not in descriptor.enum_type.values_by_number:
            raise SettingsError(
                f"default {default} is not defined by {descriptor.enum_type.full_name}"
            )

    def decode(message: Message, descriptor: FieldDescriptor) -> int:
        return getattr(message, descriptor.name)

    def assign(message: Message, descriptor: FieldDescriptor, value: int) -> None:
        if value not in descriptor.enum_type.values_by_number:
            raise SettingsError(
                f"{value} is not defined by {descriptor.enum_type.full_name}"
            )
        setattr(message, descriptor.name, value)

    return _new_field(prototype, path, default, validate, decode, assign)
