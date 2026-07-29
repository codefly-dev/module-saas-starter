# Module services

The module contains eight first-class Codefly services. `marketing` and
`frontend` are separate Next.js applications: marketing owns public
apex/`www` content, while frontend remains the authenticated product behind
`auth-sidecar`.

Run the complete local dependency graph from `module/`:

```sh
codefly run service --fixture dev-admin
```

Service manifests are generated from
`deployment/topology.bindings.codefly.yaml`; do not edit them by hand. See
`../DEPLOYMENT_TOPOLOGY.md` for the service graph and
`marketing/README.md` for the public runtime and extraction contract.
