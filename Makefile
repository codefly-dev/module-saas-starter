.PHONY: unit unit-go unit-frontend gates catalog-check role-plan playground-check playground-journeys scenarios verify verify-host

# Optional machine-local selection (GO, CODEFLY, NODE); no paths or
# credentials in Git.
-include local.mk
export CODEFLY

# Every Go module here is independent and built with no workspace
# (module/AGENTS.md, "Go suites"). GO may name a wrapper, GOTESTFLAGS passes
# the rest (-p 4, -run).
GO ?= go
GOENV = GOWORK=off
# The frontend's Node, selected by mise; NODE= runs the tree's own.
NODE ?= mise exec node@24 --
ACCOUNTS = module/services/accounts/code
FRONTEND = module/services/frontend/code

# Everything a change here passes before it is proposed, on this machine
# first: the unit tests and the tree gates, the role plan, the playground's
# host-free checks, the permission scenarios on the real store, and accounts
# as a host of the authority interface on that store (`verify-host`). It is
# the local half of .github/workflows; what it leaves to CI is listed in
# RELEASE_GATES.md and summarized under `verify` below.
#
# Left to CI, because each needs the pinned Codefly release, a registry
# credential, the merge base or a container build: `codefly ci run` (every
# service's lint, the DB-backed accounts suites other than the scenarios, the
# frontend pipeline tests and build, the store and vault suites), golangci-lint,
# the supply-chain audit and SBOM, the image builds, the module package and its
# generators, the host-interface conformance suite at authority main (the
# suite at the pinned authority is `verify-host`, here), the
# scoped-pool and store-baseline qualifications on native PostgreSQL, the
# migration history against the merge reference, the marketing isolation
# build, the kit-version and release-contract gates, the commit-identity and
# record-naming checks, and the repository-root module agent's tests.
verify: unit role-plan playground-check scenarios verify-host

unit: unit-go unit-frontend gates

# The Go suites that start nothing: accounts built and vetted under both of
# its tag sets and tested under `pure` (the default set's DB-backed packages
# start Codefly-managed stores: `scenarios` runs the permission ones, CI the
# rest), the gateway, the shared frontend payload and the repository tools.
unit-go:
	cd $(ACCOUNTS) && $(GOENV) $(GO) build ./... && $(GOENV) $(GO) vet ./... && $(GOENV) $(GO) vet -tags pure ./...
	cd $(ACCOUNTS) && $(GOENV) $(GO) test -tags pure -count=1 $(GOTESTFLAGS) ./...
	cd module/services/auth-gateway/code && $(GOENV) $(GO) vet ./... && $(GOENV) $(GO) test -count=1 $(GOTESTFLAGS) ./...
	cd module/frontendpayload && $(GOENV) $(GO) test -race -count=1 $(GOTESTFLAGS) ./... && $(GOENV) $(GO) vet ./...
	cd module/tools && $(GOENV) $(GO) vet ./... && $(GOENV) $(GO) test -count=1 $(GOTESTFLAGS) ./...

# The frontend's types and its unit tests (the `pure` project: the `pipeline`
# one starts a Codefly dependency graph and is CI's). Dependencies are
# installed when absent, never reinstalled under a running frontend.
unit-frontend:
	cd $(FRONTEND) && { test -d node_modules || $(NODE) npm ci --no-audit --no-fund; } && \
	  $(NODE) npm run typecheck && $(NODE) npm run test:pure

# The gates CI runs that read only the tree: tenant RLS coverage and up/down
# pairing of the migrations, the boundary and naming gates, each gate's own
# tests, and the generated catalogs reproduced from their inputs.
gates: catalog-check
	$(NODE) node --test module/tools/rls-migration-gate.test.mjs module/tools/migration-pairing-gate.test.mjs \
	  module/tools/naming-gate.test.mjs module/tools/test-targets-gate.test.mjs module/tools/agent-docs-links.test.mjs
	$(NODE) node module/tools/rls-migration-gate.mjs check
	$(NODE) node module/tools/migration-pairing-gate.mjs check
	$(NODE) node module/tools/test-targets-gate.mjs check
	scripts/check-boundaries.sh
	$(NODE) node module/tools/naming-gate.mjs check

# The authorization matrix and the catalogs generated from the service
# catalog (gateway routes, REST surface, frontend catalogs, topology), run
# with no change and held to the committed tree: drift is a generator, a pin
# or an input that moved, never a diff to tidy (AGENTS.md).
CATALOG_OUTPUTS = module/services/accounts/AUTHZ_MATRIX.md module/services/accounts/generated \
	module/services/accounts/openapi module/services/accounts/code/pkg/adapters module/services/accounts/code/pkg/cataloggen/testdata \
	module/services/auth-gateway/code module/services/frontend/generated module/services/frontend/code/src/gen \
	module/deployment/generated
catalog-check:
	cd $(ACCOUNTS) && $(GOENV) $(GO) generate ./pkg/business ./pkg/cataloggen
	git diff --exit-code -- $(CATALOG_OUTPUTS)
	@test -z "$$(git status --porcelain --untracked-files=all -- $(CATALOG_OUTPUTS))" || \
	  { echo "the generators wrote files the tree does not carry:"; git status --porcelain --untracked-files=all -- $(CATALOG_OUTPUTS); exit 1; }

# The role plan, with no host running: every procedure accounts declares,
# against an administrator (Dana), a member (Carol), a person with no role
# (Bob), the platform administrator and a signed-out visitor. Each cell is
# read off the declared policy and held to the real admission chain; a refused
# cell has a decider (admission, the handler's gate, or a named rule) or the
# plan fails. With it, the declared floor against each handler's gates, and
# the webhook and data source handlers called by the same people.
role-plan:
	cd $(ACCOUNTS) && $(GOENV) $(GO) test -tags pure -count=1 $(GOTESTFLAGS) ./pkg/adapters \
	  -run 'TestRolePlan|TestConnectAccessMatrix|TestDeclaredPolicyEnforcedInLockstep|TestEveryDeclaredPermissionIsEnforcedOrNamesItsRule|TestEveryDurableGrantIsWithheldFromAnImpersonatedSession|TestRestrictedProceduresAreDeniedOnlyWhileImpersonating'

# The playground's checks that need no host: the role plan, the journeys'
# cast against the fixture the host is started with, and the journeys
# compiling, so one that no longer builds fails here and not against a host.
playground-check: role-plan
	bash -n scripts/playground-check.sh
	cd module/tools && $(GOENV) $(GO) test -count=1 ./playground && $(GOENV) $(GO) vet -tags playground ./playground

# The host-only playground journeys (module/PLAYGROUND.md), against a host
# already started with `codefly run service --fixture dev-admin`. Not part of
# `verify`: nothing here starts that host. PLAYGROUND_SCOPE names the run.
playground-journeys:
	GO='$(GO)' scripts/playground-check.sh $(if $(PLAYGROUND_SCOPE),--naming-scope $(PLAYGROUND_SCOPE))

# The permission scenarios on the real store, with no host: the access matrix
# (the operations people administer the host with, each through its real
# handler, by the platform administrator, Dana, Carol, Bob of another
# organization and a signed-out visitor) and the suites beside it that hold
# tenancy, platform roles and impersonation to real rows. The test starts its
# own PostgreSQL and Vault through Codefly and removes them. Needs Docker and
# a codefly on PATH.
scenarios:
	cd $(ACCOUNTS) && $(GOENV) $(GO) test -count=1 -timeout 30m $(GOTESTFLAGS) ./pkg/business \
	  -run 'TestAccessMatrix|TestAProfileIsWritten|TestDirectory_|PlatformAdmin|TestAssignRoleRequires|TestGrantScopeRefuses|Impersonat'

# accounts as a host of the authority interface, on the real store: the shared
# host-interface conformance suite at the authority revision go.mod pins, with
# the cases beyond it (accounts' own Work Context mints held to what the
# shared core would issue), and the store contract the authority core's store
# meets over accounts' Postgres. Both are excluded by `-tags pure`, so `unit`
# never starts them. Each test starts its own PostgreSQL through Codefly and
# removes it; the suite has measured five to nineteen minutes here and is held to
# its own ceiling (conformanceCeiling). Needs Docker and a codefly on PATH.
verify-host:
	cd $(ACCOUNTS) && $(GOENV) $(GO) test -count=1 -timeout 30m $(GOTESTFLAGS) ./pkg/adapters \
	  -run '^TestAccountsPassesTheHostInterfaceConformanceSuite$$'
	cd $(ACCOUNTS) && $(GOENV) $(GO) test -count=1 -timeout 30m $(GOTESTFLAGS) ./pkg/infra \
	  -run '^TestTheAuthorityCoreStoreMeetsTheStoreContract$$'
