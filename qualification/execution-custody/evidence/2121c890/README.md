# Tenant TLS mount after upstream integration

Clean source `2121c890b2d2d17f2d765d825b45565eec365cb4` merges current main into the
reviewed mount candidate to resolve the base-manifest conflict that prevented PR
CI. The tenant implementation/source hashes are unchanged from c93c1998; main's
new datasource/Vault error changes are retained. The canonical manifest was
regenerated and verified from a clean detached tree (2,351 files).

The focused real normal-host tenant TLS suite passes again against this source:
actual PostgreSQL JWT sessions/authority, Redis revocation and native grpc-go
through normal projection/construction/lifecycle; opt-in, bind rollback,
StartTask/ExchangeAudience, trust/auth/exposure negatives and original-parent
shutdown/rebind recovery. The report pins the test executable, normal executable,
all127 migrations, fixture images and direct source hashes. Existing normal
REST/internal-gRPC/Connect/catalog and mount-guard race regressions, vet and normal
executable build pass again. Topology parity passed after the three assertion
updates retained in b095f58e; the merge did not alter topology.

Run `python3 qualification/execution-custody/run.py --tenant-mount`. See the
c93c1998 evidence README for the exact fixture boundary. This is neither a full
managed bootstrap nor external sign-in/deployment proof. No publication, cloud or
credential mutation occurred. The final wrapper changes only evidence/integrity.
