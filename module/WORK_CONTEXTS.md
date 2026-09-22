
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

## A worked installed operation binding

Two composed modules: `example-consumer` runs work on a person's behalf,
`example-producer` owns an invocable resource. The composition wants the first
to reach the second for one exact resource and nothing else.

`example-producer` contributes the vocabulary first — a
`permissions.codefly.yaml` validated by
`contracts/permissions/v1/permissions-contribution.schema.json`, naming the
resource and the actions it governs. The host never invents that vocabulary; a
resource kind nobody contributed is a string the composition made up.

```yaml
schema: codefly/saas/permissions-contribution/v1
namespace: example-producer
permissions:
  - name: example-producer.profiles:invoke
    resource: example-producer.profiles
    action: invoke
  - name: example-producer.profiles:read
    resource: example-producer.profiles
    action: read
```

The composition then declares the binding on the **calling** principal in
`module-capabilities/MODULE_PRINCIPALS`:

```json
{
  "example-consumer": {
    "tenant": "019f6bf7-5b4b-74e5-8c17-092259bb1661",
    "operation_audiences": {
      "run": {
        "audience": "example-producer",
        "invoke_scopes": [
          {
            "resource_kind": "example-producer.profiles",
            "actions": ["invoke", "read"],
            "resource_ids": ["sha256:9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08"]
          }
        ],
        "lookup_scopes": [
          {
            "resource_kind": "example-producer.profiles",
            "actions": ["read"],
            "resource_ids": ["sha256:9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08"]
          }
        ]
      }
    }
  },
  "example-producer": {
    "tenant": "019f6bf7-5b4b-74e5-8c17-092259bb1661",
    "resources": ["example-producer.profiles"]
  }
}
```

`example-consumer` redeems it with `ExchangeDelegatedOperationAudience`,
supplying its retained parent, the binding id `run`, and whether this is an
invoke or a lookup. It supplies no audience, scope, action, resource id or
lifetime: every one of those is read from the entry above. The parent must
address `example-consumer`'s own installed prefix and tenant, and must already
admit the installed scopes — installation attenuates a viewer's authority, it
never confers new authority.

Three properties of the entry are worth reading twice. `audience` may not equal
the caller's own prefix, so a binding cannot be a self-grant. `resource_ids`
pins the exact resource; omitting it widens the child to every resource of that
kind, and `"*"` is refused rather than treated as a wildcard. And because the
grant is re-read on every call, deleting the `run` entry revokes the exchange
immediately — including in the policy recheck that runs after signing — rather
than when the last issued child expires.

The transport all of this is carried on, and what a client library must and
must not require of it, is [INTERNAL_TRANSPORT.md](./INTERNAL_TRANSPORT.md).
