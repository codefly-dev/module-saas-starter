# Local development fixtures

Codefly selects the fixture with `--fixture`. Accounts seeds identities, roles,
teams and grants from the selected YAML before opening listeners. Optional
`installations` are permitted only in local execution and applied afterward, once
signed delivery is available.
Successful service startup is not proof that installation setup completed: look
for `fixture installation setup completed`; failure is logged explicitly and
the setup attempt is bounded to five minutes and cancelled at shutdown.

Existing `assignments` name an `agent_identifier`. `team_assignments` instead
name a declared `team`, with the same `org`, `role` and optional `scope` fields.
Collections may add `teams` alongside individual `readers`, and a paired
`resource_type`/`resource_id` to place an owner's opaque resource. Placement does
not create content in another module.

```yaml
installations:
  - org: ExampleCorp
    installer: owner@example.com
    publisher: example
    name: probe
    version: 1.0.0
    role: Example execution
    allowed_audiences: [example]
    allowed_scopes: [example-records]
    teams:
      - {name: Example team, role: Use example}
```

The organization and installer must declare pinned IDs; the installer must be
an organization administrator. Roles and teams must be declared in the same
fixture. Before installation, the stored provider identity must resolve to the
declared installer UUID; legacy ID drift is refused rather than attributing consent
to whoever occupies that UUID. The exact accepted release resolves to an immutable installation
target. Accounts calls its existing installation operation, observes the normal
worker acknowledgement, and only then exposes the installation to teams. It
does not create active installations, run callbacks, retry failed installations,
or grant additional consent to repair incompatible retained state.

On restart, identical active consent is checked through the existing operation;
a pending installation is observed until acknowledgement before that check.
The current installation service emits creation events again for identical active
replay; retained identity is stable, but audit/event history is not a no-op.
Failed or revoked installations and replacement targets require explicit
lifecycle action or fresh demo state. Selected fixtures reapply their declared
ordinary roles and collection/team grants at startup; they are not continuously
reconciled. Test revocation without restarting a fixture that declares the grant.

For native local development, the existing Accounts `DEV_FIXTURE_PATH` override
and frontend `FIXTURES_DIR` override can select one consumer-owned seed through
Codefly's per-service `--set` flags. Both must point to the same file/name.
Keep the builtin selected fixture's identity metadata unchanged: native delivery
also reads its platform administrator from the host's builtin fixture. Verify
that equality against the selected host version. These filesystem overrides
are not a container mount or a packaged fixture contribution.
