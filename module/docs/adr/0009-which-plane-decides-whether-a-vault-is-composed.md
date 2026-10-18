# ADR 0009: Which plane decides whether this module composes a Vault

- Status: Proposed
- Date: 2026-10-17
- Task: decides the design question in #1128, raised by a hosted cell's first
  boot of 0.0.98. Authorizes no schema change in this repository and opens one
  named obligation on the CLI, under Consequences.

## Context

accounts holds two keys and selects which service holds each at runtime, from
the `key-service` configuration group: `KEY_SERVICE_BACKEND` for the envelope
key and the keyed hash, `KEY_SERVICE_SIGNING_BACKEND` for the Ed25519 signing
key ([KEY_ROTATION.md](../../KEY_ROTATION.md)). On
`KEY_SERVICE_BACKEND=kms` with `KEY_SERVICE_SIGNING_BACKEND=kms-wrapped` it
opens **no Vault connection at all**: `pkg/keyservice/load.go` builds one only
inside the `vault` sealer, the wrapped signing key is a ciphertext in
configuration that a Cloud KMS key unwraps at boot, and `requireKeyCustody` is
gated on the signing backend being `vault`.

The module's **inventory** is a different plane. `module.codefly.yaml`'s
`services:` list and each environment's block in the composing workspace decide
what is rendered, scheduled, given a volume and given reach — before any process
exists to read a configuration group.

Those two planes had no declaration in common. So a cell whose keys live in
Cloud KMS was still composed a `vault` service: rendered, scheduled, refusing to
start without the auto-unseal seal such a cell is forbidden to hold
(`vault: no auto-unseal seal is configured…`), and crash-looping forever as a
permanently Degraded workload that nothing read. Meanwhile accounts' `vault`
configuration group stayed declared, so the composition also had to answer for
`VAULT_AUTH_METHOD` and the AppRole credential of a binding to a service it
never reached.

### What the tree already held

- `module/services/vault` is composed unconditionally by the base's `services:`
  list, and the module agent copies only the services a consumer's own
  `module.codefly.yaml` declares (`copyModuleSource`, `preserveInventory`).
- `regenerateServiceManifests` already pruned a `service-dependencies` entry
  naming a service the consumer did not compose. It did **not** prune the
  configuration group through which that service's key service is reached.
- `managed-services.vault: {kind: cell-vault}` already dropped the module's own
  Vault from an environment and bound the cell's instead
  ([deployment/README.md](../../deployment/README.md)). It answers "the Vault is
  *that* one", never "there is no Vault".
- The renderer could only place a dependency in two states, in-cluster or
  managed. A dependency that was neither failed generation outright —
  `dependency accounts -> vault has no in-cluster service or managed handoff` —
  so dropping the Vault from an environment was unrenderable, and the only
  renderable choice was to schedule one.

### The three shapes on the table

1. **The module declares the service conditionally on the key-service
   selection**, read from the configuration group at render time.
2. **`vault` moves out of the module** into each composition that selects it.
3. **The selection is declared in the manifest plane**, where the inventory is
   decided, and the module derives the Vault from it.

The choice is load-bearing because the first makes a rendered inventory a
function of a file the configuration plane owns and may legitimately not
deliver, and the second makes every Vault-selecting composition re-author a
service this module exists to ship.

## Decision

**Shape 3, at two grains, and the second grain is blocked on the CLI.**

**Per composition — the service inventory.** A composition with no Vault in any
environment leaves `vault` out of its `services:`. That is read as the
*selection* — the only half of it the CLI parses — and the `vault`
**configuration group is withdrawn by the selection**, not by the service's
absence. This grain is live.

The distinction is the one thing in this decision that is easy to get wrong, so
it is worth stating twice: a configuration group is required by the **backend an
environment selected**, because a group is how a process *reaches* that backend.
It is not required by a workload being in the inventory. The two coincide for
this module, since the only purpose of its `vault` service is to be that
backend — which is why keying the group off the service produced the right
answer for the wrong reason. They part immediately in the shape that already
exists: a cell whose Vault is the **cell's own**, bound through the `cell-vault`
handoff, renders no Vault of this module's and needs every key in the group.

Because a group is declared once per composition and a selection is per
environment, the reduction is agreement: the group is withdrawn only when **no**
environment selects the vault backend (`compositionSelectsVault`). Both the
manifest edit and the overlay render read the same `resolveKeyService`, so they
cannot disagree about which key service a composition uses. An environment that
*states* `key-service: vault` while the composition composes no Vault is refused
by name — the one contradiction an unstated selection cannot be read as.

**Per environment — `key-service: vault | kms`.** `kms` withdraws the service
from that environment's inventory, withdraws **every dependency edge** to it,
and refuses a `vault` managed service beside it. Withdrawn is a **third state**
beside in-cluster and managed, and it has to be: a handoff renders an
`ExternalName` Service, and a cell whose keys are in a cloud key service has no
address for one to point at, so granting reach would authorize a dial that does
not happen.

The two words are exactly the two `KEY_SERVICE_BACKEND` takes, so one word means
one thing on both sides of the deployment.

### Why not read the configuration group at render time (shape 1)

A group's values are the configuration plane's to deliver per environment, and
the renderer has never read one. A file that may legitimately be absent would
make the render fall back to composing a Vault — which **is** the failure this
decision exists to remove, reintroduced as a default.

A configuration **profile directory** cannot carry it either. Core reads each
configuration directory from the first profile in the chain that directory
holds, *and from that one alone* — profiles are never merged. Creating
`configurations/<cell>/` to hold a single key would stop every other
workspace-level group for that environment resolving through the profile it
resolves through today.

### Why not move `vault` out of the module (shape 2)

The module's own `vault` service is what makes a first local run work:
`codefly run service --fixture dev-admin` reaches it over loopback with a token,
and `module/GETTING_STARTED.md` promises that. Moving it out would make every
Vault-selecting composition re-author the same service — a second mechanism for
a job one seam already does — and would leave a base-synced consumer meeting the
absence as a boot failure. A cell that *runs* its own Vault already has
`cell-vault` for that, and #1006 is the separate question of binding it.

### The line this draws, and what it does not touch

`key-service: vault`, and an **unstated** selection, keep every existing
behaviour: the module's own durable Vault in-cluster, or the cell's through the
handoff. Local is untouched. No service manifest, migration or generated
artifact changes.

### Revisiting

Two things would reopen this.

**The CLI accepting the field.** An unstated selection on a deployed environment
*should* be refused by name, exactly as `keyservice.selectedBackend` refuses the
same unstated fact at boot — omission is precisely what put a Vault on a cell
that reads none. It is not refused, because the field cannot be written yet (see
Consequences), and refusing an omission would refuse every composition instead
of the one mistake. When the field lands, that becomes one line in
`resolveKeyService`, and
`TestAnUnstatedKeyServiceStillComposesTheVault` inverts.

**`KEY_SERVICE_SIGNING_BACKEND=kms` becoming supported.** It is refused by name
today because three consumers are handed the signing key rather than a signer to
call ([KEY_ROTATION.md](../../KEY_ROTATION.md)). That changes which backends are
selectable; it does not change which plane decides the inventory, so this
decision stands.

## Consequences

**An obligation on the CLI, and a pre-existing bug it also fixes.**
`workspace.codefly.yaml`'s environment block is parsed by the CLI's own
`pkg/environments.Environment`, strictly and **recursively**, and that type has
no extension seam — core's `resources.Environment` carries an inline
`Extensions map[string]YAMLValue` for host-owned declarations, and the CLI does
not use it here. Verified against codefly 0.1.181:

```
field key-service not found in type environments.Environment
field auth-mode not found in type environments.EnvironmentManagedService
field egress-namespace not found in type environments.EnvironmentManagedService
```

Each fails the **whole workspace load**, so a key on one environment breaks
every environment's run. The second and third are **not** this decision's: this
module has documented `auth-mode` and `egress-namespace` on the `cell-vault`
handoff since that kind landed, and the module agent requires both, yet no live
workspace can declare either — which is why `workspace.codefly.yaml` keeps its
`managed-services` example commented out. The CLI owes `KeyService` on
`Environment` and `AuthMode`/`EgressNamespace`/`InstanceConnectionName` on
`EnvironmentManagedService`.

**Until then the per-environment grain is inert and the per-composition grain is
the whole answer.** A composition that wants the Vault locally and not on its
cell has no declaration available, and must choose per composition.

**Neither grain reaches a composition that pins a release.** Both are
declarations in the consumer's own files — its `module.codefly.yaml` services
list, or `key-service:` on an environment of its `workspace.codefly.yaml`. A
composition that pins a published release of this module receives the fixed
module package and keeps whatever its own composition file says, and that file
still composes `vault` until somebody edits it. So moving a pin changes nothing
here: the per-composition route needs one edit in the consumer, and the
per-environment route needs the CLI field first. A pinned cell on
`KEY_SERVICE_BACKEND=kms` therefore still renders the `vault` workload, and
accounts still reads nothing from it, until that edit happens. The module cannot
close that half from here — it would have to write a consumer's composition
file, which it never does.

**A composition on `kms` stops declaring the `vault` groups.** Selecting the
cloud key service everywhere withdraws `VAULT_ADDR`, `VAULT_AUTH_METHOD`,
`VAULT_APPROLE_MOUNT`, `VAULT_KEY_CUSTODY` and the `vault.secret.env` AppRole
pair, and needs no `may-be-empty` marker for any of them.

**The mismatch is refused at boot, by its own reason.** A hosted accounts that
selects the vault backend for either family while the `vault` group carries
nothing refuses to start, naming the group and the selection that requires it
(`requireVaultGroup`). Without it the first key read answered for the whole
group: `VAULT_KEY_CUSTODY`'s refusal told an operator to set a seeding command
in a group that had been withdrawn.

**Per-environment group delivery remains inexpressible.** A composition that
keeps the `vault` service for its local environment keeps the group in every
environment. That is harmless — accounts reads no key of it on the `kms`
selection — but "this group only in these environments" is not something a
service manifest can say.
