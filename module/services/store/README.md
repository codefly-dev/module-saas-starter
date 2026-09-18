# Welcome to Service store!

## Structure of the service

The application is structured as follows:

- `./providers`
- `./src`

## Deployment prerequisites

### Postgres extensions

The baseline runs `CREATE EXTENSION IF NOT EXISTS` for `uuid-ossp`, `ltree` and
`btree_gist`. Managed Postgres services gate which extensions may be created. On
Azure Database for PostgreSQL Flexible Server, they must be allow-listed in the
`azure.extensions` server parameter before the baseline runs; otherwise it fails
with an error beginning
`extension "uuid-ossp" is not allow-listed …`. The consumer infra sets this
parameter, but the requirement is invisible until the first failed migration.

### Migration principal

The baseline creates the five runtime roles and transfers ownership of the
SECURITY DEFINER functions to `app_control_plane` and `app_job_worker`. The
migration user does not need to be a superuser, but it needs two distinct
authorities:

1. `CREATEROLE`, to create the runtime roles and receive `SET` membership in
   them — the ownership transfers require `SET ROLE`.
2. Control over the `public` schema's grants — ownership of the schema, or
   `CREATE ON SCHEMA public WITH GRANT OPTION` — because each transfer
   briefly grants `CREATE ON SCHEMA public` to the incoming owner and revokes
   it again.
