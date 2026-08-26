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
in-cluster services, ingress routes, and managed-service handoffs. It records no
repository, revision, or Argo resource.

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
    service: auth-sidecar
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

Managed-capable environments — `eks` on AWS and `aks` on Azure — declare each
module-owned managed service under `managed-services`. The module generates an
`ExternalName` Service and topology-derived egress policy. Optional
`secret-references` generate ExternalSecret objects containing only provider
keys and SecretStore references. Supported `kind` values are `elasticache`,
`rds-postgresql`, `s3`, `secrets-manager`, and `azure-postgres-flexible`. The
Azure `ExternalSecret` handoff shape is still in flux under infra's passwordless
direction, so the worked example below stays on the stable AWS shape:

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

No cloud-provider behavior is added to the generic Postgres, Redis, S3, or
Vault service plugins.

The installed Starter topology includes an independently deployable marketing
service. Local hosts and production domains belong to the consumer's
environment contract; the module generator derives their exact gateway policy
without shipping a placeholder domain patch.

## Configuration & environment variables

Runtime environment variables reach a service through a **configuration group**:
a named `.env` file that one or more services opt into. This is the committed,
portable path — use it for any value that should travel with the repo. For a
one-off, uncommitted override on a single `codefly run`, use the `--set` flag
instead (see [AGENTS.md](../../AGENTS.md#passing-an-environment-variable-to-a-service)).

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
`workspace_configuration_dependencies` in
[`topology.bindings.codefly.yaml`](./topology.bindings.codefly.yaml) — the source
of truth for the service graph. That list is rendered into the generated
`services/<svc>/service.codefly.yaml`. The frontend, for example:

```yaml
  - name: frontend
    workspace_configuration_dependencies:
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
   the frontend's `workspace_configuration_dependencies` in
   `topology.bindings.codefly.yaml`. Do **not** hand-edit
   `services/frontend/service.codefly.yaml`; it is generated.

3. **Regenerate** the per-service manifests from the bindings so
   `service.codefly.yaml` picks up the new dependency (the same module render
   described in [AGENTS.md](../../AGENTS.md#agent-version-pins)).

4. **Refresh the base-integrity manifest.** `topology.bindings.codefly.yaml`,
   this README, and the generated `service.codefly.yaml` are base-tracked;
   editing them without regenerating `module/tools/base-manifest.json` fails CI.
   See [AGENTS.md](../../AGENTS.md#base-file-integrity-manifest--the-easy-gate-to-trip).

Steps 2–4 are only for the committed path. For a throwaway local value, skip them
and use `--set` (AGENTS.md) or — frontend only — a gitignored `.env*.local` under
`module/services/frontend/code/`.

The skin resolver spans all three tiers: `FRONTEND_SKIN_JSON` (an inline JSON
descriptor) for a quick `--set`-free experiment via `.env*.local`,
`FRONTEND_SKIN_DIR` (a directory of `<host>.json` / `default.json`) for a
configuration group, and a mounted `frontend-skin` ConfigMap in a deployed
environment.
