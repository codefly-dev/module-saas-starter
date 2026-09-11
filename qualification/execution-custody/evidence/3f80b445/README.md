# Clean-source Accounts subprocess evidence

Exact source `3f80b44585bc3e6c2d22c7322a7535d0f7e18076`, baseline
`fa31c3440f546c31076ab0cdebfbb36c23e41324`. This adds the actual bounded
`cmd/custody-qualification` process to the earlier component candidate.

[report.json](report.json) pins source/package/migration/image/binary hashes and
31 passing cases (including nested substitutions). [acceptance.log](acceptance.log)
is sanitized case/status output. Actual owner-authenticated canonical gRPC
issuance, separate revision gRPC/JWKS, real JWT sessions, worker mTLS, Vault
Transit/ACL and scoped PostgreSQL run together. The process registers custody,
terminates, restarts with original storage/key inputs, and returns the exact same
original Task child through owner recovery without original parent bytes in the
caller, followed by worker-only bounded lookup exchange. No worker gets the parent
or the fixture owner's access JWT. The tests also verify all substitution,
conflict, outage, rotation, revision/revocation and expiry cases from the prior pack.

All 127 owning migrations apply; migration 131 passes down/up. Vault's existing
ciphertext decrypts after actual Vault process restart. Runtime custody grants
and forced RLS are checked against live PostgreSQL; UPDATE is confined to envelope
erasure. The complete adapter/client race suite and vet pass, alongside 29 RLS
gate tests, naming, migration pairing and clean-worktree base integrity.

Reproduce from clean source: `python3 qualification/execution-custody/run.py`.
The exact cached local Docker image IDs in the report are required. The process
bootstrap and complete private configuration schema are in
[the runbook](../../../../module/services/accounts/EXECUTION_CUSTODY.md).

This is actual Accounts-process/local-private-storage qualification, not a
consumer Task/worker/model joined effect-count proof or managed hosting.
Full Codefly CI/infrastructure TestMain was not run locally; upstream release
gates remain required. No production credential, IAM/grant or cloud operation
and no paid provider call occurred.
