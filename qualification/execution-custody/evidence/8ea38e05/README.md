# Clean-source private custody evidence

Implementation `8ea38e055cb14a0dd93ec41b3d6b484f26332696`, baseline
`fa31c3440f546c31076ab0cdebfbb36c23e41324`. Run on 2026-09-11 with a clean
worktree; [report.json](report.json) pins code, dependencies, local image IDs
and all 127 owning migrations. Migration 131 also passed down/up on the
disposable database after acceptance.

The actual Accounts JWTMinter/session store, mTLS listener and private client,
Vault Transit/scoped ACL and verified PostgreSQL reader/control-plane adapter
were exercised under race detection. [acceptance.log](acceptance.log) contains
only case names/status. It covers original Task child convergence, concurrent
registration, no-parent registration recovery, owner/tenant/task/session/worker/
profile/digest substitution, same-lineage different-parent denial, bounded
execute/read-only children, broker/store reconstruction, rotation, outages,
revision and actor-chain revocation, expiry/tombstones and physical role limits.

The full adapters/client race suite, vet, 29 static RLS gate tests, migration
pairing, naming and clean-checkout base integrity pass. The standalone
infrastructure TestMain requires Codefly service orchestration, unavailable in
this local shell; the real local harness exercises the modified store and
migrations directly and upstream mandatory gates remain required.

This is local component/private-storage qualification. The consumer's actual
execution/worker/downstream process join and independently counted original-key
receipt recovery remain separate evidence, as do all managed-hosting gates.
No production apply, IAM mutation, production credential change or paid call.

Reproduce from the clean implementation source with
`python3 qualification/execution-custody/run.py`. Docker must already contain the
exact local image IDs in the report; no mutable image substitution is accepted.
See [the runbook](../../../../module/services/accounts/EXECUTION_CUSTODY.md).
