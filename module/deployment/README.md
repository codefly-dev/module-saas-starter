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
- explicit AWS managed-service endpoints, network CIDRs, and external secret
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
        {"service": "store", "awsKind": "rds-postgresql", "externalName": "store.internal.example.com"}
      ]
    }
  ]
}
```

Managed AWS services drop out of the in-cluster `services` list and appear as
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
      kustomization.yaml      # sets `namespace:` and references base + ingress
      ingress.yaml            # Gateway + VirtualService (host-bearing; omitted without ingress)
      base/
        kustomization.yaml
        namespace.yaml        # identity-neutral placeholder name
        resource-quota.yaml
        limit-range.yaml
        network-policy.yaml
        istio-mtls.yaml
        destination-rules.yaml
        handoffs/
          <managed-service>.yaml
```

The base is identity-neutral: its Namespace object carries a placeholder name and
no host is baked into it. Each environment overlay supplies the identity — its
`namespace:` transformer names the namespace (and places every resource in it),
and its `ingress.yaml` owns the host-bearing Gateway and VirtualService — so one
base can back many namespaces. An overlay contains module-owned Kubernetes
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
environment contract; the module generator derives their exact gateway policy
without shipping a placeholder domain patch.
