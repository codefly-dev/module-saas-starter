# Module-level deployment

The module agent generates `deployment/kustomize` from the installed product's
declared service inventory, deployment topology, and each declared environment.
Generation is a pure local render: it needs no Git binary, checkout, network,
GitHub token, Argo CLI, or cluster access. Module sync replaces the previous
generated tree, so legacy Starter placeholders — including any Argo
`Application`/`AppProject` files from earlier releases — are removed rather than
released to consumer ownership.

Repository publication, immutable revision selection, `AppProject`/`Application`
assembly, and Argo/Flux observation belong to the CLI/server promotion driver,
not to this plugin. The plugin emits a transport-neutral bundle; the promotion
driver composes whatever repository transport it uses from that bundle.

Generation consumes:

- the installed module's declared service inventory and deployment topology;
- each declared environment's cluster kind, namespace, and exact ingress routes;
- explicit managed-service endpoints, network CIDRs, and external secret
  references.

Any `gitops:` publication block still present in a migrated workspace is
ignored; no repository, branch, revision, or checkout field is read.

## Bundle manifest

`bundle.json` is the typed, transport-neutral output. It records the module
identity and, per declared environment, the module-owned overlay path plus the
placement metadata a promotion driver needs — namespace, cluster kind,
in-cluster services, ingress routes, managed-service handoffs, and deploy Jobs.
It records no repository, revision, or Argo resource.

```json
{
  "schemaVersion": "codefly.dev/module-bundle/v1",
  "module": "users",
  "namespace": "users",
  "serviceEntry": "forge-edge",
  "environments": [
    {
      "name": "aws",
      "namespace": "users-aws",
      "cluster": "eks",
      "resourcePath": "overlays/aws",
      "services": ["accounts", "forge-edge", "frontend"],
      "ingress": [
        {"name": "product", "service": "forge-edge", "endpoint": "rest", "port": 8080, "hosts": ["app.example.com"]}
      ],
      "managedServiceHandoffs": [
        {"service": "store", "kind": "rds-postgresql", "externalName": "store.internal.example.com"}
      ],
      "deployJobs": [
        {"name": "role-catalog-import", "service": "accounts", "command": "role-catalog-import", "catalog": "deployment/generated/contributed-roles.json", "force": true, "writes": {"service": "store", "endpoint": "tcp", "port": 5432}, "after": ["store"]}
      ]
    }
  ]
}
```

Managed services drop out of the in-cluster `services` list and appear as
handoffs instead. Generation rejects unsupported cluster kinds, undeclared or
duplicate environments, managed services that are not declared module services,
and any object outside the module-owned apiVersion allowlist (a boundary test
also fails if runtime code imports/executes Git or names Argo/Flux/repository
transport configuration).

## Deploy Jobs

A `deploy_jobs` topology entry is a store-writing one-shot the driver runs as a
deploy step. Unlike a service's self-serving `bootstrap_job_endpoints` Job —
which reaches only the service's own endpoints — a deploy Job runs one service's
image but `writes` to a dependency it declares, consuming a generated artifact.
The module ships `role-catalog-import`: it runs the `accounts` image but writes
the composed built-in role catalog into the `store`, `after` the store's own
migration. Each bundle `deployJobs` entry resolves the `catalog` artifact path,
the target `service`/`endpoint`/`port`, the `force` flag, and the ordering; the
driver mounts the catalog, connects the target, runs `command`, and fails the
promotion if it exits non-zero. Re-running an unchanged catalog is an empty
no-op, so the step is idempotent. Generation rejects a deploy Job whose catalog
artifact is absent, whose write target is not a declared dependency of the
running service, that references an undeclared service, or that writes to a
migration-bearing target (`bootstrap_job_endpoints`) without ordering `after` it
— an import that races the migration would write against a schema that does not
yet exist. In an environment where the target is a managed handoff the driver
owns the out-of-cluster reach, so no in-cluster reach policy is rendered —
mirroring the store's own bootstrap authority.

The bundle is transport-neutral, but the driver honours a small contract the
generated reach policies assume. The Job pod runs under the **running service's**
ServiceAccount (`service`, here `accounts`): the mesh already authorises that
identity to the write target because generation requires `service` to declare
the dependency, so no new mesh policy is minted for the Job. The Kubernetes Job
is named after the entry's `name` (`role-catalog-import`) so the controller's
auto-set `job-name` pod label matches the rendered `NetworkPolicy` selector — the
same convention a `bootstrap_job_endpoints` Job follows. `after` is inclusive of
each named service's own bring-up, its migration/bootstrap Job included, which is
why `after: ["store"]` is sufficient to sequence the import behind the store
migration.

## Generated layout

```text
deployment/kustomize/
  bundle.json
  overlays/
    <environment>/
      kustomization.yaml      # sets `namespace:` and references base + namespace + ingress
      namespace.yaml          # Namespace object, named for the environment
      ingress.yaml            # Gateway + VirtualService (host-bearing; omitted without ingress)
      base/
        kustomization.yaml
        resource-quota.yaml
        limit-range.yaml
        network-policy.yaml
        istio-mtls.yaml
        destination-rules.yaml
        handoffs/
          <managed-service>.yaml
```

The base is identity-neutral: it carries neither the Namespace object nor a host.
Each environment overlay supplies the identity — its `namespace.yaml` names the
namespace, its `ingress.yaml` owns the host-bearing Gateway and VirtualService,
and its `namespace:` transformer places every base resource in that namespace —
so one base can back many namespaces. An overlay contains module-owned Kubernetes
objects only — namespace, resource-quota, limit-range, NetworkPolicies, Istio
mTLS/gateway, and managed `ExternalName`/`ExternalSecret` handoffs. It never
contains a Secret, an `AppProject`, or an `Application`. Render an environment
locally with:

```sh
kubectl kustomize modules/<module>/deployment/kustomize/overlays/<environment>
```

## Ingress routes

An environment may map exact hosts to public module-interface endpoints:

```yaml
ingress:
  - name: marketing
    service: marketing
    endpoint: http
    hosts:
      - www.example.com
      - docs.example.com
  - name: product
    service: auth-gateway
    endpoint: rest
    hosts:
      - app.example.com
```

The generator rejects duplicate or wildcard hosts, managed or undeclared
targets, and endpoints that are not public module interfaces; no catch-all host
is generated. Ingress is optional: an environment that declares no `ingress:`
renders module-owned baseline resources (namespace, quotas, NetworkPolicies,
mTLS, handoffs) without a Gateway/VirtualService, and its bundle entry records
an empty ingress list.

## Managed-service handoffs

Managed-capable environments — `eks` on AWS, `aks` on Azure, and `gke` on GCP —
declare each module-owned managed service under `managed-services`. The module
generates an `ExternalName` Service and topology-derived egress policy. Optional
`secret-references` generate ExternalSecret objects containing only provider
keys and SecretStore references. Supported `kind` values are `elasticache`,
`rds-postgresql`, `s3`, `secrets-manager`, `azure-postgres-flexible`, and
`cloud-sql-postgres`. The Azure `ExternalSecret` handoff shape is still in flux
under infra's passwordless direction, so the worked example below stays on the
stable AWS shape:

```yaml
managed-services:
  store:
    kind: rds-postgresql
    external-name: identity.cluster.example.com
    egress-cidrs:
      - 10.42.0.0/24
    secret-references:
      - name: store-runtime
        remote-key: products/identity/store
        secret-store:
          name: aws-secrets-manager
          kind: ClusterSecretStore
```

### Authentication mode

`auth-mode` states how callers authenticate to a managed service. It defaults to
`password`: the connection secret reaches the workload through
`secret-references`, which is the shape every AWS and Azure kind above uses. A
handoff in the default mode records no `authMode` key, so a bundle for an
existing environment is unchanged. `external-identity` is the passwordless shape
— the pod authenticates as its own workload identity, so the instance issues no
connection secret, the bundle handoff records the mode for the promotion driver,
and the overlay renders no ExternalSecret. Declaring `secret-references`
alongside it is rejected.

Under `external-identity` the module also renders an egress NetworkPolicy from
each caller to the node-local instance metadata endpoint
(`169.254.169.254/32`, TCP 80), which is where every major cloud serves
workload-identity tokens. The baseline denies all egress and the public-egress
policy excepts link-local, so without that rule the mode would have no path to
the credential it is defined by and the failure would land at connection time
rather than at generation.

The rendered shape is a **direct** authenticated connection to the instance's
DNS name — an `ExternalName` Service plus the declared egress CIDRs on the
declared ports. The module does not render a Cloud SQL connector/Auth Proxy
sidecar, and a deployment that adds one needs egress this module does not
generate. `instance-connection-name` is carried in the bundle for the promotion
driver, which owns anything outside the cluster; it is the
`project:region:instance` coordinate (the legacy domain-scoped
`domain:project:region:instance` form is also accepted) and is not derivable
from a DNS name. Because a silent password default would have the driver project
a secret an IAM-only instance never issued, the kind requires `auth-mode` to be
stated rather than inherited:

```yaml
managed-services:
  store:
    kind: cloud-sql-postgres
    external-name: store.identity.internal.example.com
    auth-mode: external-identity
    instance-connection-name: identity-prod:us-central1:store
    egress-cidrs:
      - 10.42.0.0/24
```

No cloud-provider behavior is added to the generic Postgres, Redis, S3, or
Vault service plugins.

### The cell's Vault: `kind: cell-vault`

Every other kind above names something outside the cluster. `cell-vault` names a
Vault the **cell already runs inside the same cluster**, in a namespace of its
own, and it exists because of what this module's own `vault` service holds: the
host's Ed25519 signing key and the Transit key that seals every API key,
connector credential, MFA secret and WebAuthn credential. A product must reach
that Vault by name rather than inherit whichever one the composition happened to
render.

**A cell on a cloud key service needs this only for the signing key.** Selecting
`KEY_SERVICE_BACKEND=kms` in the `key-service` group moves the envelope key and
the keyed hash to the cell's cloud key-management service — non-exportable keys,
reached with the workload's own cloud identity, with key names and no credential
in the configuration. The signing key cannot move there yet, so a hosted cell
still declares this kind for that one key; `module/KEY_ROTATION.md`, "Why `kms`
cannot hold the signing key yet", says what has to change upstream first. When it
does, a cell selecting `kms` for both families declares no `cell-vault` at all.

```yaml
managed-services:
  vault:
    kind: cell-vault
    external-name: vault.vault.svc.cluster.local
    auth-mode: external-identity
    egress-namespace: vault
```

Declaring it drops this module's `vault` service from that environment's
in-cluster inventory entirely — no StatefulSet, no PVC, no token — and renders
an `ExternalName` Service plus one egress NetworkPolicy from each declared
caller to the Vault's namespace on the dependency's declared ports.

Three constraints are enforced rather than defaulted, because each one, left to
a default, reproduces a failure this kind exists to prevent:

- **`auth-mode: external-identity` is required**, and `password` is refused by
  name. The handoff itself projects no connection secret: the caller
  authenticates with a credential the configuration plane delivers into its own
  secret group, not with one this handoff renders beside the Service. A password
  default would have the promotion driver project a secret nothing issued.
  `secret-references` alongside it is rejected, as for every kind in this mode.
- **`egress-namespace` is required and `egress-cidrs` is refused.** A pod's
  address is not stable across reschedules, and the pod CIDR that would cover it
  authorizes every workload in the cluster. The destination is selected by
  `kubernetes.io/metadata.name` instead.
- **No metadata-endpoint egress is rendered**, unlike the cloud passwordless
  kinds. There is no workload-identity token to fetch over the network: the
  credential arrives as a secret.

#### What the cell supplies

**Nothing in this binding is a file, and nothing mounts anything.** In-cluster
transport security belongs to the mesh, so a cell's Vault listens in-mesh
without TLS of its own: there is no certificate to anchor, no CA bundle, no
projected token, and no Vault-agent sidecar. Codefly delivers values and
secrets. Through the `vault` configuration group:

| Key | Hosted value |
| --- | --- |
| `VAULT_ADDR` | `http://<vault>.<namespace>.svc.cluster.local:8200` |
| `VAULT_AUTH_METHOD` | `approle` |
| `VAULT_APPROLE_MOUNT` | the auth mount; `approle` when empty |
| `VAULT_KEY_CUSTODY` | the command the cell uses to seed the signing key; **required** outside local |

and through the `vault` **secret** group, delivered like every other accounts
secret:

| Key | Hosted value |
| --- | --- |
| `VAULT_APPROLE_ROLE_ID` | identifies the Vault role |
| `VAULT_APPROLE_SECRET_ID` | proves the holder may assume it |

accounts logs in with that credential, holds the Vault token it receives in
memory only, renews it before its lease runs out, and logs in again when renewal
fails or Vault refuses it. No long-lived Vault token, and never a root token, is
delivered to the pod. accounts refuses every other binding outside the local
environment, naming the key that is missing or wrong.

#### Why the address is plaintext, and what admits it

A cell Vault has no https URL to give, so the address is `http://`. Cleartext off
loopback is admitted by exactly one thing — the composition's assertion in the
`internal-transport` group that every in-cluster hop is carried by a mutually
authenticated mesh:

```dotenv
# configurations/<profile>/internal-transport.env
mesh-protected=true
```

and then only for an in-cluster Kubernetes Service address, which is exactly
`<service>.<namespace>.svc` or that followed by the default cluster domain
`cluster.local`.

Neither half is enough alone, which is the whole point: a hostname says nothing
about whether a mesh wraps the wire, and the assertion covers only what a mesh
can cover. So an external name, a bare IP, a short name — or an `svc` label
buried in somebody else's domain, like `vault.vault.svc.example.com`, which
resolves on the public internet — stays refused with the assertion set. The
suffix is **matched**, never inferred from whatever follows `svc`: nothing in the
platform supplies an authoritative cluster domain, so the trusted suffix is
Kubernetes' default and only that, and a cell with a custom cluster domain uses
the unqualified three-label form, which resolves in-cluster under any domain.

Only the exact value `true` asserts it; unset, empty and `false` keep plaintext
refused, and any other value fails startup rather than being read as either
answer. The group, key, value handling and remedy sentence are the ones the
composed modules apply to their own in-cluster hops, so a cell asserts it once —
with one deliberate difference: this matcher is stricter about the suffix, and
that is not drift to reconcile by loosening it.

**Loopback admits plaintext in a local run only.** A deployed runtime gets no
loopback exemption: a cell naming a loopback Vault would be reading its secrets
from something inside its own pod — the in-memory store this binding exists to
stop — and would reach it without the composition asserting anything at all.

`VAULT_ALLOW_INSECURE_HTTP` is gone. It was a blanket per-service opt-in that no
address could qualify; this replaces it with a rule that names what it covers.

#### The Vault policy the role needs

`read` on `secret/data/jwt-signing-key` and `update` on each of
`transit/encrypt/api-keys`, `transit/decrypt/api-keys` and
`transit/hmac/api-keys`. Granting `transit/keys/api-keys` instead is the trap:
that path manages key metadata, so the signing-key read succeeds while every
encrypt call answers 403. Creating the key belongs to the Vault service's own
provisioning, not to this role.

**A cell that would rather run this module's Vault** keeps it in its in-cluster
inventory and declares nothing here. The vault agent then renders the durable
shape — `vault server` with integrated raft storage on a retained
PersistentVolumeClaim, auto-unsealed by the seal the environment supplies — for
every deployed profile; the in-memory `vault server -dev` shape is reachable only
from the ephemeral local-apply render. That durable server refuses to start
without an auto-unseal seal, so the seal must be supplied as the vault service's
own per-service configuration
([../services/vault/configurations/aws/vault.env](../services/vault/configurations/aws/vault.env)).
Its listener is plaintext too, so the same mesh assertion admits it.

The installed Starter topology includes an independently deployable marketing
service. Local hosts and production domains belong to the consumer's
environment contract; the module generator derives their exact gateway policy
without shipping a placeholder domain patch.

## Configuration & environment variables

Runtime environment variables reach a service through a **configuration group**:
a named `.env` file that one or more services opt into. This is the committed,
portable path — use it for any value that should travel with the repo. For a
one-off, uncommitted override on a single `codefly run`, use the `--set` flag
instead.

### Layout

```text
configurations/
  local/                        # default profile (`codefly run service`)
    error-tracking.env           # non-secret group vars (committed)
    internal-auth.secret.env     # secret group vars (committed, dev-only values)
  local-dogfood/                # profile selected by `--env local-dogfood`
    error-tracking.env.example        # template; the real .env is generated + gitignored
    error-tracking.secret.env.example
```

- A **group** is just a name — `error-tracking`, `identity`, `billing`, … It
  "exists" by having a matching `<group>.env` file and being referenced by a
  service; there is no top-level registry of group names.
- **`<group>.env`** holds non-secret values; **`<group>.secret.env`** holds
  secrets. On the `local` profile both are committed with dev-only values. On
  `local-dogfood` only the `.example` templates are committed — the real
  `.env` / `.secret.env` are produced by the setup scripts and are gitignored,
  so real provider secrets never land in git (see
  [../../LOCAL_DOGFOODING.md](../../LOCAL_DOGFOODING.md)).
- Both plain server vars and Next.js `NEXT_PUBLIC_*` (client/build-inlined) vars
  live in these files and flow through the same path — e.g. the frontend's
  `NEXT_PUBLIC_SENTRY_DSN` comes from `error-tracking.env`.

### Wiring a group to a service

A service receives a group's vars only if it lists that group under
`workspace-configuration-dependencies` in its own
`services/<svc>/service.codefly.yaml`. The frontend, for example:

```yaml
workspace-configuration-dependencies:
    - abuse-protection
    - error-tracking
    - identity
    - internal-auth
    - product-analytics
```

which is why `NEXT_PUBLIC_SENTRY_DSN` (from `error-tracking.env`) reaches it.

### Which profile applies

| Command | Profile | Files |
| --- | --- | --- |
| `codefly run service` | `local` | `configurations/local/*` |
| `codefly run service --env local-dogfood` | `local-dogfood` | `configurations/local-dogfood/*` |

Production configuration is not a repo file — it is supplied by the deploy
target, so there is no `configurations/production/`.

### Worked example: add a new env var to a service

To give the frontend `FRONTEND_SKIN_DIR` (the SSR skin resolver reads it to load
a mounted skin descriptor) as a committed value for local runs:

1. **Pick or create a group.** Add the var to a group the frontend already
   depends on, or create a new group file — e.g.
   `configurations/local/skin.env`:

   ```dotenv
   FRONTEND_SKIN_DIR=/etc/codefly/skin
   ```

2. **Wire the group** — only needed for a *new* group — by adding its name under
   `workspace-configuration-dependencies` in
   `services/frontend/service.codefly.yaml`.

Step 2 is only for the committed path. For a throwaway local value, skip them
and use `--set` or — frontend only — a gitignored `.env*.local` under
`module/services/frontend/code/`.

The skin resolver spans all three tiers: `FRONTEND_SKIN_JSON` (an inline JSON
descriptor) for a quick `--set`-free experiment via `.env*.local`,
`FRONTEND_SKIN_DIR` (a directory of `<host>.json` / `default.json`) for a
configuration group, and a mounted `frontend-skin` ConfigMap in a deployed
environment.

## Endpoint declaration compatibility

Authored endpoints use Codefly’s separate visibility and location fields. The
host’s cross-module exports declare `visibility: internal` and the explicit
`allow-modules: ["*"]` ceiling they previously had; private listeners stay
private. The store endpoint retains external location independently of its
private module visibility. No endpoint address or application permission changes.

The host deployment catalog keeps its existing `MODULE` and `EXTERNAL`
categories. Both topology readers translate the declared policy into those
categories, refusing retired input spellings and any narrower allow-list the
catalog cannot represent. They never discard an allow-list and widen access.
The canonical catalog generation leaves the effective policy artifacts unchanged.
A composition must also select compatible manifests for every other module;
this migration does not permit editing an immutable module cache.
