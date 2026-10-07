# Module services

`module/module.codefly.yaml` is the inventory: every first-class Codefly
service this module builds is listed there, and that list is what the package
manifest is held to. No count is written here — a number in prose is one two
branches each bump to the same value and merge into a total that is silently
wrong, and nothing enforces it.

`marketing` and `frontend` are separate Next.js applications: marketing owns
public apex/`www` content, while frontend remains the authenticated product
behind `auth-gateway`. `policy-log` is the independent signed witness for
authority changes; it is private to this module, has no public endpoint, and
belongs to the warehouse's trust domain rather than to the database whose
narrowings it witnesses — see `policy-log/README.md`.

Run the complete local dependency graph from `module/`:

```sh
codefly run service --fixture dev-admin
```

Each `service.codefly.yaml` is authored and is the only place its agent and
its deployment facts (`spec.deployment`) are named. See
`../DEPLOYMENT_TOPOLOGY.md` for the service graph and
`marketing/README.md` for the public runtime and extraction contract.
