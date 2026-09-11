# Reviewed transport correction: clean local qualification

Exact implementation `b77a0570d3dee14ba144c0b3c1af653aec3273b2` closes the
independently reproduced verified-TLS case-alias/session-parameter override.
The query and parsed runtime parameter allowlists now reject ROLE/Role,
session_authorization, options/endpoint aliases and embedded NUL values.
Supported application labels and pool settings pass. Legacy default, Unix profile
and fixed role hooks are unchanged by this correction.

[Parser race tests](transport-parser.log), the [actual Accounts Unix-socket proof](unix-store.log)
and all31 real PostgreSQL16/Vault/Accounts subprocess cases pass at clean source.
[Report](report.json) pins all127 migrations, final fixture binary and source hashes.
The Unix proof uses disposable PostgreSQL14.19, all migrations, two actual socket
identities, store reconstruction and private-custody denial for tenant and all
three fixed worker pools. Normal host/adapter/client race and vet also pass.

No managed proxy/IAM/remote TLS or hosted bootstrap is claimed. This supersedes
the pre-correction5da candidate for release; its evidence is retained separately.
A final image must use this corrected source after mandatory review/check gates.
