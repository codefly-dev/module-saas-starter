# Module-level deployment

The module agent generates `deployment/kustomize` from the installed product
and an immutable service render. The canonical module does not ship an Argo CD
tree, so module sync removes legacy Starter placeholders instead of releasing
them to consumer ownership.

Generation consumes:

- the workspace name and exact GitOps repository, checkout, owned path, and
  revision;
- every environment's cluster kind and namespace;
- the installed module's declared service inventory and deployment topology;
- the service overlays already present at the immutable revision; and
- explicit AWS managed-service endpoints, network CIDRs, and external secret
  references.

The declared checkout defaults to the workspace root. Set `gitops.checkout`
when the rendered GitOps repository is a separate checkout. Its `origin` must
match `gitops.repo-url`.

## Required sequence

The revision is a rendered input, not the branch used for a future render:

1. Render each service overlay into
   `<gitops.path>/deployments/modules/<module>/services/<service>/overlays/<environment>`.
2. Ensure remote environments omit overlays for services handed to the cloud
   provider.
3. Commit that tree in the declared GitOps checkout.
4. Set `gitops.revision` to that commit SHA, or to a signed immutable tag for a
   remote environment.
5. Run the module generator.
6. Apply `deployment/kustomize/overlays/<environment>` as the bootstrap.

Generation fails if an Application path is absent from the selected commit.
For k3d, kind, and minikube, the selected commit must also equal the checkout's
current `HEAD`. This prevents an Application from pointing at the pre-render
commit.

The current CLI's deterministic render/publish flow is tracked by
`codefly-dev/cli#152`. Until that flow invokes module generation after the
snapshot is committed, callers must perform the sequence above explicitly.

## Generated layout

```text
deployment/kustomize/
  inventory.json
  overlays/
    <environment>/
      kustomization.yaml
      resources/
        namespace.yaml
        project.yaml
        resource-quota.yaml
        limit-range.yaml
        network-policy.yaml
        istio-mtls.yaml
        istio-gateway.yaml
        handoffs/
          <managed-service>.yaml
      applications/
        <in-cluster-service>.yaml
```

The generator renders every committed service overlay before writing the
bootstrap. It rejects missing or extra service paths, managed services that
still have an in-cluster remote overlay, cluster-scoped child resources,
unresolved placeholders, and Kubernetes Secrets anywhere in the owned tree.
This closes the module boundary even while Core's versioned reference-only
secret renderer is completed in `codefly-dev/core#101`.

The AppProject repository and destination are exact. Its namespaced resource
allowlist is derived from the Kubernetes kinds in the immutable child renders;
it grants no cluster-resource authority.

## AWS handoffs

EKS environments declare each module-owned managed service under
`managed-services`. The module generates an `ExternalName` Service and
topology-derived egress policy. Optional `secret-references` generate
ExternalSecret objects containing only provider keys and SecretStore
references:

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

No AWS behavior is added to the generic Postgres, Redis, S3, or Vault service
plugins.
