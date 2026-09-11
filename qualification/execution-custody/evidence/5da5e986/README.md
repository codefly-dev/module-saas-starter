# Explicit Accounts transport local proof

Exact implementation `5da5e986c873a03487e50823f701ee5438d9aa57`, base
`778b2cd47a55fd589e34902e4730cee96802be6b`.

The [Unix store proof](unix-store.log) passes on disposable local PostgreSQL14.19,
with all127 owning migrations, two distinct Unix socket/principal bindings, actual
Accounts scoped factory, private custody denial to direct/tenant access, unchanged
record recovery after reconstruction, and all three fixed worker pools denied
private custody. [Parser tests](transport-parser.log) cover explicit verify-full,
legacy default compatibility and rejection of proxy ambiguity/ambient credentials.

All31 original real PostgreSQL16/Vault/Accounts subprocess cases also pass at this
clean source; [report](report.json) pins migrations, binary and follow-up source
hashes. This is not a Cloud SQL proxy/IAM/remote TLS or managed Accounts boot proof.

Reproduction commands and profile semantics are in the Accounts custody runbook.
Full release checks/review and an image including this change remain required.
