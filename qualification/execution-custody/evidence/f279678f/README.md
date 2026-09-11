# Production mount candidate: local qualification

Exact clean source `f279678ff750f95ddeac5c78631c839f5a911a99`, baseline
`fa31c3440f546c31076ab0cdebfbb36c23e41324`.

The [report](report.json) records all127 owning migration hashes,31 passing
real PostgreSQL/Vault/Accounts subprocess cases, canonical package pins and local
binary hashes. [Acceptance](acceptance.log) includes actual process replacement
and recovery of the same original Task child. Internal revision gRPC is verified
with server TLS and the separate token, without a caller client certificate.

[Production tests](production-tests.log) cover the normal opt-in mount, fail-closed
revocation prerequisite, private atomic-file projections, Vault certificate trust,
token rotation, missing-file denial and redirect denial, plus generated topology.
The normal Accounts executable builds; vet and29 RLS-gate tests pass.

Reproduce from clean source: `python3 qualification/execution-custody/run.py`.
From Accounts code: `go test -race . ./pkg/vaultconnection ./pkg/adapters
./pkg/executioncustody ./pkg/cataloggen` and `go build -trimpath -o /absolute/output/accounts .`.

This is local qualification. It does not establish external sign-in, consumer
joined effect counts, image publication, a managed endpoint or database/Vault
identity/transport. Required upstream release gates remain intact.
