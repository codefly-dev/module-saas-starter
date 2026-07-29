# Module-level deployment

Shared, module-wide Kubernetes resources for saas-starter. Per-service
manifests are rendered by each service's agent at `codefly deploy` time
(into `<workspace>/deployments/modules/saas-starter/services/<svc>/`).
The Codefly topology feeding those agents is generated from
`topology.bindings.codefly.yaml`; see `../DEPLOYMENT_TOPOLOGY.md`. This
directory also owns everything that **spans services**:

- The `saas-starter` namespace.
- The ArgoCD `AppProject` (RBAC scope for all module Applications).
- The ArgoCD `Application` per service (app-of-apps pattern).
- Generated default-deny and service-dependency `NetworkPolicy` resources.
- Shared Istio ingress/mTLS policy, quotas, limits, and environment overlays.

## Layout

```
module/deployment/
  topology.bindings.codefly.yaml         endpoint/dependency/egress source
  generated/
    service-topology.json                typed normalized topology
  kustomize/
    base/                         shared across environments
      kustomization.yaml
      namespace.yaml
      project.yaml                ArgoCD AppProject "saas-starter"
      network-policy.yaml         generated least-privilege policies
    overlays/
      local/                      k3d / kind / minikube
        kustomization.yaml
        applications/             ArgoCD Application per service
          accounts.yaml
          auth-sidecar.yaml
          frontend.yaml
          marketing.yaml
          store.yaml
          cache.yaml
          vault.yaml
          object-storage.yaml
      aws/                        EKS production
        kustomization.yaml
        applications/
          accounts.yaml
          auth-sidecar.yaml
          frontend.yaml
          marketing.yaml
          # store / cache / vault / object-storage are intentionally
          # external in prod (RDS, ElastiCache, Secrets Manager, S3).
```

The `overlays/<env>` directory names match the `env.Name` field in
`workspace.codefly.yaml` — same convention as agent-rendered per-service
overlays at `<workspace>/deployments/modules/<module>/services/<svc>/overlays/<env>/`.

## Gitops flow

1. **Render**: `codefly deploy --env <env>` (or a future `--render-only`
   flag — see "Open gap" below) writes per-service kustomize overlays
   to the workspace's `deployments/` tree.
2. **Commit**: the rendered tree is committed to a git repository
   ArgoCD watches.
3. **Bootstrap**: apply `module/deployment/kustomize/overlays/<env>`
   directly with `kubectl apply -k`. This creates the namespace, the
   AppProject, and every child Application.
4. **Sync**: ArgoCD pulls each Application's `path:` from git and
   syncs to the cluster — no further `codefly deploy` needed for
   subsequent updates.

```bash
# Bootstrap once
kubectl apply -k module/deployment/kustomize/overlays/local

# Subsequent deploys
codefly deploy --env local --render-only && \
  git add deployments/ && git commit -m "deploy: …" && git push
# ArgoCD sees the new commit and syncs.
```

## Open gap

`codefly deploy` today **applies** the rendered kustomize via `kubectl
apply` directly — it doesn't expose a `--render-only` mode that just
writes the rendered manifests to disk for git-commit. The `gitops`
loop above assumes that mode exists. Until it does:

- For local-k3d, `codefly deploy --env local` is the simpler path
  (skip ArgoCD entirely; the LocalApplyManager imports built images
  into k3d and applies manifests directly).
- For EKS, render-then-commit can be done by hand: `codefly deploy
  --env aws --dry-run` exists but emits to logs, not to disk. Need a
  `--render-only` or `--output <dir>` flag that writes the rendered
  tree without applying.

Tracked as a follow-up — once that's in, the gitops flow above is the
default for non-local environments.

## Environment-specific notes

### local (k3d)

All eight services run in-cluster. Postgres, Redis, Vault, MinIO are
deployed as StatefulSets with PVCs. Suitable for dev and integration
testing.

The gateway routes `saas.localhost`, `www.saas.localhost`, and
`docs.saas.localhost` to marketing; `app.saas.localhost` and the legacy
`localhost` fallback go through auth-sidecar to the product.

### aws (EKS)

Only the four application services (`accounts`, `auth-sidecar`, `frontend`,
`marketing`) are Applications here. The rest are AWS-managed:

- `store` → Amazon RDS for Postgres.
- `cache` → Amazon ElastiCache for Redis.
- `vault` → AWS Secrets Manager (or a dedicated Vault cluster — see
  `agents/services/vault/templates/deployment/` for the dev shape and
  swap the overlay).
- `object-storage` → Amazon S3.

The connection strings flow into the apps via Kubernetes Secrets
sourced from External Secrets Operator (which reads from Secrets
Manager / SSM Parameter Store). Wire that up in the app overlays
or via a SecretStore CRD in this module.

`overlays/aws/marketing-domains.example.patch.yaml` is a production domain
example, not an enabled default. Copy it into the overlay, replace every
example host and certificate name, then add exact authentication callback
origins. `status.example.com` remains external and must not route through this
Gateway.
