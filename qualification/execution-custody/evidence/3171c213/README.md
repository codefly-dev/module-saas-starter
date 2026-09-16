# Installed operation policies: final local qualification

Exact clean source `3171c213f22084b2b803807baa2e369bb5dfcce7` passed the
real PostgreSQL/Vault custody qualification. The evidence-only commit that adds
this directory changes no runtime, SDK, policy or test source.

The run used `python3 qualification/execution-custody/run.py` with the exact
cached images in `report.json`. It exercised authenticated registration,
multi-operation exchange across distinct audiences, exact-resource and installed
kind-wide scopes, read-only lookup attenuation, encrypted custody, process
replacement, read-only recovery, policy drift, expiry, authorization revision,
actor revocation and Vault key rotation.

Hosted qualification separately found that the Core v0.3.27 SDK correctly
refuses the pinned CLI 0.1.145 shared control channel unless the test harness
opts in. Every Accounts dependency-backed package now opts in explicitly, and
the existing package lifecycle lock keeps those sessions serialized. The
published service-agent fleet remains on its supported CLI rather than being
silently driven by a newer incompatible runtime.

This is disposable local qualification, not managed hosting or live-provider
evidence. No managed resource or paid provider was called.
