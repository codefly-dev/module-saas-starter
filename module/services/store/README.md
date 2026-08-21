# Welcome to Service store!

## Structure of the service

The application is structured as follows:

- `./providers`
- `./src`

## Deployment prerequisites

### Postgres extensions

Migration 1 runs `CREATE EXTENSION IF NOT EXISTS "uuid-ossp"`. Managed
Postgres services gate which extensions may be created. On Azure Database for
PostgreSQL Flexible Server, `uuid-ossp` must be allow-listed in the
`azure.extensions` server parameter before the first migration runs;
otherwise migration 1 fails with an error beginning
`extension "uuid-ossp" is not allow-listed …`. The consumer infra sets this
parameter, but the requirement is invisible until the first failed migration.

### Migration principal

Several migrations transfer ownership of SECURITY DEFINER functions to the
`app_control_plane` role. The migration user does not need to be a superuser,
but it needs two distinct authorities:

1. Membership in `app_control_plane`, so it can `SET ROLE` to it — the
   ownership transfer requires this. Migration 67 grants the membership to
   the migrating principal.
2. Control over the `public` schema's grants — ownership of the schema, or
   `CREATE ON SCHEMA public WITH GRANT OPTION` — because each transfer
   briefly grants `CREATE ON SCHEMA public` to the incoming owner and revokes
   it again. Migration 67 already exercises this same authority (it grants
   `USAGE` and revokes `CREATE` on `public`), so any principal that can apply
   migration 67 can apply these.
