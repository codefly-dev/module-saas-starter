
# Installed delegated audience exchange

`ModuleCapabilitiesService.ExchangeDelegatedReadAudience` is an internal,
read-only exchange for a module holding a current signed parent but no owner
bearer. The module authenticates independently with its own module Work Context
and the internal perimeter credential. Its request contains only `binding_id`
and `parent_work_context_token`. No owner, actor, target audience or scope is
caller selectable. The parent must address the authenticated module's installed
prefix and tenant; the canonical current-revision and every-hop revocation
checks run before exchange and again before returning the child. Delegated
parents require a configured durable journal. Actor ceilings and scope
attenuation use the same signer and authority path as owner audience exchange.

Installation declares an optional `read_audiences` map in the existing
`module-capabilities/MODULE_PRINCIPALS` registry, for example:

```json
{"example":{"tenant":"019f6bf7-5b4b-74e5-8c17-092259bb1661","read_audiences":{"proof":{"audience":"example-producer","scopes":[{"resource_kind":"results"}]}}}}
```

Each installed scope may additionally restrict `resource_ids`. Actions are
always exactly `read`. The incoming parent must already admit all installed
scopes; installation never confers new viewer authority. A child lives at most
60 seconds and less than the remaining parent lifetime. Owner, tenant, Task,
Session and actor/delegation chain remain unchanged. The response is secret and
must not be logged. No persisted credential or broad signing port is created.
Interactive calls can continue using the existing owner-bearer
`WorkContextService.ExchangeAudience` API.

`ModuleCapabilitiesService.ExchangeDelegatedOperationAudience` applies the same
identity, tenant, current-authority, journal, recheck, lifetime and secrecy
rules to an installed operation binding. Its request contains the parent,
`binding_id`, and a `lookup` selector only. The caller cannot supply the target
audience, scopes, actions, resource ids or TTL.

An operation binding declares canonical invoke scopes and a read-only lookup
subset:

```json
{"example":{"tenant":"019f6bf7-5b4b-74e5-8c17-092259bb1661","operation_audiences":{"generate":{"audience":"example-producer","invoke_scopes":[{"resource_kind":"results","actions":["read","write"]}],"lookup_scopes":[{"resource_kind":"results","actions":["read"]}]}}}}
```

Resource kinds, actions and resource ids are sorted and unique; wildcards are
refused. Lookup scopes must be a subset of invoke scopes and may contain only
`read`. Removing or changing the installed binding revokes the exchange on the
next call, including the post-signing policy recheck. Every verified-parent
attempt emits a credential-free `saas.module.delegated_audience_exchange`
observation with owner, effective actor, authenticated module, binding,
selected audience when known, and an `issued` or sanitized `refused` outcome.
