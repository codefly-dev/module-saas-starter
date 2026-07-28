# Module-level deployment

The module agent generates `deployment/kustomize` for the workspace that
installs the Starter. It does not copy the canonical repository's Argo CD
files. Generation consumes:

- the workspace name and `gitops.repo-url`, `gitops.path`, and
  `gitops.branch` contract;
- each declared environment's name, cluster kind, and namespace; and
- the installed module's exact declared service inventory and service paths.

Generation fails when this contract is incomplete, a service path is missing
or extra, a production revision is mutable, or the rendered policy contains a
placeholder, wildcard AppProject authority, secret data, or an unexpected
Application.

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
      applications/
        <declared in-cluster service>.yaml
```

`inventory.json` binds the workspace, module, repository, owned render path,
environment namespaces, immutable revisions, exact in-cluster services, AWS
managed-service handoffs, and the Core reference-only secret-manifest contract.
It contains references and identities only, never resolved secret bytes.

Every environment gets its own AppProject. Its source repository and
destination are single exact values. Namespace and cluster resource authority
is an enumerated allowlist; no wildcard is emitted.

## Revision policy

For a local k3d, kind, or minikube environment, a configured branch is accepted
only when it resolves to the checked-out harness `HEAD`; the generated
Applications contain that full commit SHA. A remote EKS environment accepts a
full commit SHA or a locally verified signed tag. Signed tags are resolved to
their commit before rendering, so Argo CD never receives a floating revision.

## Service inventory and AWS handoffs

Local overlays contain one Application for every declared module service. EKS
overlays keep application services in-cluster and record the Starter's
`store`, `cache`, `object-storage`, and `vault` dependencies as RDS,
ElastiCache, S3, and Secrets Manager handoffs. Provider behavior remains at
this module boundary rather than entering generic service plugins.

Service agents render their owned paths under:

```text
<gitops.path>/deployments/modules/<module>/services/<service>/overlays/<environment>
```

The module generator validates that the declarations and installed service
directories match exactly before it emits Applications.

## GitOps flow

1. Run the module generator after declaring the workspace GitOps contract.
2. Render service overlays with `codefly deploy module --env <name>
   --render-only`.
3. Validate and commit the owned deployment tree.
4. Bootstrap the generated module overlay with `kubectl apply -k
   deployment/kustomize/overlays/<name>`.
5. Argo CD reconciles each Application at the immutable revision.

Direct local apply and remote publish/observation policy remain CLI-owned.
