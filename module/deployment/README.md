# Module-level deployment

The module agent generates `deployment/kustomize` from the installed product
and an immutable service render. The canonical module does not ship an Argo CD
tree, so module sync removes legacy Starter placeholders instead of releasing
them to consumer ownership.

Generation consumes:

- the CLI's canonical render inventory at the reviewed revision, including its
  exact owned path, module/service graph, file digests, and Core output evidence;
- the selected environment's cluster kind, namespace, and optional exact ingress
  routes;
- the installed module's declared service inventory and deployment topology;
- explicit AWS managed-service endpoints, network CIDRs, and external secret
  references.

The declared checkout defaults to the workspace root. Set `gitops.checkout`
when the rendered GitOps repository is a separate checkout. Its `origin` must
match `gitops.repo-url`. `gitops.inventory` names the exact repository-relative
`.codefly-render.json` selected by the CLI; the inventory's `ownedPath` is the
only source of Application paths.

## CLI publication contract

The CLI owns the render, review, immutable publication, and module-generation
sequence. Module generation starts only after `gitops.revision` selects the
reviewed service snapshot:

```yaml
gitops:
  repo-url: git@github.com:my-org/platform-config.git
  path: clusters/codefly
  branch: main
  revision: 0123456789abcdef0123456789abcdef01234567
  checkout: ../platform-config
  inventory: clusters/codefly/deployments/modules/users/.codefly-render.json
  environment: aws
```

The version 2 CLI inventory is canonical JSON. It records the selected module,
environment, AppProject, owned repository path, sorted exact service graph,
sorted file hashes and sizes, and aggregate digest. Every in-cluster service
records its exact path and the returned Core Kubernetes output:

```json
{
  "module": "users",
  "service": "accounts",
  "path": "services/accounts/overlays/aws",
  "output": {
    "kind": "KUSTOMIZE",
    "profile": "KUBERNETES_OUTPUT_PROFILE_PROMOTABLE_GITOPS_V1",
    "contractVersion": "codefly.dev/kubernetes-manifest/v1",
    "validation": {
      "staticValidation": "STATUS_PASSED",
      "serverSideValidation": "STATUS_PASSED",
      "promotable": true,
      "violations": []
    }
  }
}
```

Managed AWS services remain in the graph with `managed: true` and no in-cluster
path or output. Generation rejects a graph that differs from the installed
module, any file outside that graph, any digest mismatch, and any Core response
that is not promotable under the released v1 contract.

For k3d qualification, `repo-url` may be an absolute credential-free `file://`
remote used by the CLI. Argo CD cannot read that host path, so the CLI also
sets an exact `fetch-repo-url` served on `host.k3d.internal`:

```yaml
gitops:
  repo-url: file:///tmp/codefly-gitops/platform-config.git
  fetch-repo-url: http://host.k3d.internal:8080/platform-config.git
```

Applications use only the fetch URL and the resolved 40-character commit.
Local generation additionally requires the selected revision to equal checkout
`HEAD`. Remote generation requires a full commit SHA or a locally verified
signed tag. The checkout origin, committed inventory, every inventoried byte,
and every rendered Application path are verified before output is replaced.

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

The tree contains only `gitops.environment`; generation replaces the previous
tree instead of retaining Applications or AppProjects from other environments.
The generator renders every inventoried service path before writing the
bootstrap. It rejects missing or extra services, managed services with
in-cluster output, cluster-scoped child resources, unresolved placeholders,
and Kubernetes Secrets anywhere in the owned tree.

The AppProject repository and destination are exact. Its namespaced resource
allowlist is derived from the Kubernetes kinds in the immutable child renders;
it grants no cluster-resource authority.

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
targets, and endpoints that are not public module interfaces. Every selected
environment must declare at least one exact route; no catch-all host is
generated.

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

The installed Starter topology includes an independently deployable marketing
service. Local hosts and production domains belong to the consumer's
environment contract; the module generator derives their exact Applications
and gateway policy without shipping a placeholder domain patch.
