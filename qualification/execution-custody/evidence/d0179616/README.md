# Installed operation policies: local qualification

Exact clean implementation source `d01796167fc703b9e8b9e93c8deffb8bb383653c` passed
the real PostgreSQL/Vault custody qualification. The evidence-only commit that
adds this directory changes no runtime, SDK, policy or test source.

The run used `python3 qualification/execution-custody/run.py` with the exact
cached images in `report.json`. It exercised authenticated registration,
multi-operation exchange across distinct audiences, exact-resource and installed
kind-wide scopes, read-only lookup attenuation, encrypted custody, process
replacement, read-only recovery, policy drift, expiry, authorization revision,
actor revocation and Vault key rotation. It also ran the normal Accounts custody
subprocess and verified that request scope and TTL injection are rejected.

Public SDK race/vet, Accounts host and adapter race/vet, boundary, naming and
base-integrity checks passed. Hosted qualification separately caught the now
satisfied Core transport tripwire; only that assertion was removed, while the
released-agent discovery/completeness gate remains.

This is disposable local qualification, not managed hosting or live-provider
evidence. No managed resource or paid provider was called.
