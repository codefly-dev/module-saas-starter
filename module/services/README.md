# Module services

The module contains eight first-class Codefly services. `marketing` and
`frontend` are separate Next.js applications: marketing owns public
apex/`www` content, while frontend remains the authenticated product behind
`auth-gateway`.

Run the complete local dependency graph from `module/`:

```sh
codefly run service --fixture dev-admin
```

Each `service.codefly.yaml` is authored and is the only place its agent and
its deployment facts (`spec.deployment`) are named. See
`../DEPLOYMENT_TOPOLOGY.md` for the service graph and
`marketing/README.md` for the public runtime and extraction contract.
