# Normal tenant TLS mount: focused local proof

Implementation c93c1998cd5b7e08f22c999714e37fc7048c7c7b passed at clean source. The
following b095f58e240d01846eeaecd7e2d1b602b04fb97d commit changes only three topology
test inventory assertions for the newly declared endpoint; generated topology
parity then passes. All five qualified source hashes and127 migration hashes
match the final candidate. The evidence wrapper adds no runtime implementation.

Run `python3 qualification/execution-custody/run.py --tenant-mount` from the repo
root with exact cached PostgreSQL/Vault/Redis images from report.json. This uses a
hashed race-enabled normal-host test binary, actual PostgreSQL JWT sessions and
WorkContext authority, real Redis session revocation, and the ordinary host's
projection/construction/bind/shutdown functions. Network discovery alone is
replaced with allocated loopback listeners. The disposable database uses existing
loopback fixture DSNs; this is not a managed database transport or complete
Codefly bootstrap proof. Vault is the existing persistent local fixture.

Acceptance proves explicit opt-in, atomic bind rollback, native grpc-go over
verified TLS1.3, owner-authenticated StartTask/ExchangeAudience, missing/forged and
revision-only denial, CA/hostname/TLS-version/plaintext denial, internal/tenant
exposure separation, shutdown/rebind preserving the original parent and horizon,
and actual Redis session revocation. Existing normal REST/internal-gRPC, Connect
catalog and listener-guard race tests pass. Topology parity, vet and the normal
Accounts executable build pass; binary digest retained in report.json.

No new issuer or authorization algorithm, migrations, live credentials, cloud
operation, provider call or artifact publication. No full managed bootstrap,
external sign-in or hosted chat acceptance is claimed. Earlier custody and joined
reports remain unchanged. The previous deployment image lacks this new mount.
