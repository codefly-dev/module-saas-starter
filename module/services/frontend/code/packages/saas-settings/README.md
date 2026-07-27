# `@codefly/saas-settings`

Schema-agnostic settings helpers owned by the SaaS Starter.

Products define or override their Settings protobuf, generate the normal
TypeScript bindings, and build a concrete typed catalog with
`defineSettingsField`. The helpers do not import a product schema and do not
know how settings are persisted.

Use `clear_mask` paths to restore defaults. Never encode reset as JSON `null`.
