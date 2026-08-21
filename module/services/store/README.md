# Welcome to Service store!

## Structure of the service

The application is structured as follows:

- `./providers`
- `./src`

## Deployment prerequisites

### Postgres extensions

Migration 1 runs `CREATE EXTENSION "uuid-ossp"`. Managed Postgres services
gate which extensions may be created. On Azure Database for PostgreSQL
Flexible Server, `uuid-ossp` must be allow-listed in the `azure.extensions`
server parameter before the first migration runs; otherwise migration 1
fails with `extension "uuid-ossp" is not allow-listed`. The consumer infra
sets this parameter, but the requirement is invisible until the first failed
migration.

### Migration principal

Several migrations transfer ownership of SECURITY DEFINER functions to the
`app_control_plane` role. The migration user does not need to be a superuser,
but it must be able to `SET ROLE app_control_plane` — i.e. a member of that
role, which migration 67 grants to the migrating principal.
