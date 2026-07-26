# Typed settings

## Ownership and product override

Settings are a SaaS Starter capability, not a Codefly SDK capability.

- `services/accounts/code/pkg/settings` is the schema-agnostic Go runtime.
- `services/frontend/code/packages/saas-settings` is the schema-agnostic
  TypeScript runtime and public frontend/plugin package.
- `services/accounts/proto/saas/accounts/v1/user_settings.proto` is the
  concrete product schema.
- Generated Go and TypeScript protobuf files are the concrete application
  model.

A product such as Warden owns the concrete proto at that same module overlay
path and generates its Go and TypeScript bindings from the product's actual
settings. The generic runtimes are copied from SaaS Starter unchanged. They
accept generated protobuf message types and contain no import of a generated
Starter or Warden schema. Product-specific fields belong in the product proto
and its typed field catalog; they must never be added to Codefly SDK or to the
generic runtime.

Every SaaS Starter includes one common per-user settings contract:

- `appearance.theme`
- `regional.locale`, `regional.timezone`, `regional.date_format`, `regional.time_format`
- `email.product`, `email.marketing`, `email.security`, `email.weekly_digest`
- `notifications.in_app`, `notifications.push`, `notifications.sound`

The generated `UserSettings` protobuf is the only application model. Postgres
stores sparse ProtoJSON in `users.settings`; raw JSON is confined to the
Postgres adapter.

## Presence and defaults

- An absent scalar inherits the typed field catalog default.
- A present scalar is an explicit override, including `false`, `""`, or `0`.
- ProtoJSON `null` is treated as absent, not as a third scalar state.
- Clearing an override uses `UpdateUserSettingsRequest.clear_mask`.
- API reads materialize all common defaults without writing them to Postgres.

This lets a new user keep `{}` in storage while every API client receives a
complete, typed settings document.

The update semantics are deliberately presence-based:

| Stored leaf | Patch leaf | Clear path | Result |
| --- | --- | --- | --- |
| absent | absent | no | remains absent; reads resolve the catalog default |
| explicit value | absent | no | explicit value is preserved |
| any | explicit value, including `false`, `""`, or `0` | no | patch value wins |
| any | absent | yes | override is deleted; reads resolve the catalog default |
| any | present | yes | rejected as ambiguous |

Missing and present-but-empty parent messages have the same leaf semantics.
Reads never materialize either case. Writes create every missing parent, and
clears prune parents only when no sibling or unknown protobuf field remains.
Do not use truthiness, SQL scalar `COALESCE`, or JSON `null` to implement this
contract; use protobuf presence and the typed field helpers.

## Go

Use the product catalog in `pkg/usersettings.Fields`; never traverse protobuf
parents or address JSON keys directly:

```go
document := &accountsv1.UserSettings{}

theme, err := usersettings.Fields.Appearance.Theme.Get(document)
err = usersettings.Fields.Email.Product.Set(document, false)
path := usersettings.Fields.Appearance.Theme.Path()
```

`Get` returns the default without mutating `document`. `Set` creates missing
parents. `Clear` removes the field and prunes empty parents. `Lookup` preserves
the distinction between absent and explicitly present zero values.

## Frontend

Use the matching product catalog. It binds the generated `UserSettings` and
patch types once through `@codefly/saas-settings`; the reusable field behavior
does not change when the concrete proto changes:

```ts
const theme = Settings.appearance.theme.get(settings);
const patch = Settings.appearance.theme.patch(ThemePreference.DARK);
const clearPaths = [Settings.appearance.theme.path];
```

The frontend sends generated protobuf messages. It does not construct or parse
the stored JSON representation.

## Extending settings

Add the field to the product protobuf first, keep scalar presence explicit,
regenerate Go and TypeScript, then expose it in the product's two typed
catalogs with one default. The schema-agnostic Go and TypeScript runtimes must
remain byte-for-byte reusable. Add tests for missing parents,
present-but-empty parents, every explicit zero value, repeated default
application, clear/reset pruning with siblings, ProtoJSON null/round trips,
concurrent sparse updates, and real Postgres recursive merge behavior. A
schema addition needs code generation but no new JSONB column.
