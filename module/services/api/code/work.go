package main

import (
	"backend/fixtures"
	"backend/pkg/business"
	"context"

	codefly "github.com/codefly-dev/sdk-go"

	"backend/pkg/infra"

	"backend/pkg/adapters"
)

func init() {
	WithWork(doWork)
}

func doWork(ctx context.Context) (Clean, error) {
	store, err := infra.NewPostgresStore(ctx)
	if err != nil {
		return nil, err
	}

	service, err := business.NewService(store)
	if err != nil {
		return nil, err
	}

	// Initialize vault-backed services
	vaultClient, err := infra.NewVaultClient(ctx)
	if err == nil {
		service.SetHasher(vaultClient)
	}

	tokenService, err := infra.NewTokenService(ctx)
	if err == nil {
		service.SetTokenSigner(tokenService)
	}

	auditEmitter := business.NewAsyncAuditEmitter(store, 1024)
	service.SetAuditEmitter(auditEmitter)

	entitlementChecker := business.NewDefaultEntitlementChecker(store)
	service.SetEntitlementChecker(entitlementChecker)

	featureChecker := business.NewDefaultFeatureChecker(store, entitlementChecker)
	service.SetFeatureChecker(featureChecker)

	adapters.WithService(service)

	if codefly.WithFixture("simple") {
		err = fixtures.Simple(ctx, service)
		if err != nil {
			return nil, err
		}
	}

	if codefly.WithFixture("dev-admin") {
		err = fixtures.DevAdmin(ctx, service)
		if err != nil {
			return nil, err
		}
	}

	return func() {
		store.Close()
	}, nil
}
