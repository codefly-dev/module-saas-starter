package infra

import (
	"accounts/pkg/jobs"
	"context"
	"encoding/json"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/require"
)

type authorityLockExecutor struct {
	QueryExecutor
	locks int
}

func (e *authorityLockExecutor) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	e.locks++
	return pgconn.CommandTag{}, nil
}
func TestInstallationJobEffectReusesOnlyExactHeldAuthority(t *testing.T) {
	held := jobs.ModuleAuthority{PrincipalID: "principal", OrganizationID: "org", InstallationID: "installation", InstallationRevision: 2, BindingID: "binding"}
	use := &moduleAuthorityUse{installations: map[string]jobs.ModuleAuthority{held.InstallationID: held}}
	ctx := context.WithValue(context.Background(), moduleAuthorityUseKey{}, use)
	executor := &authorityLockExecutor{}
	raw, err := json.Marshal(held.Attributes())
	require.NoError(t, err)
	require.NoError(t, lockModuleJobAuthority(ctx, executor, raw))
	require.Zero(t, executor.locks, "a second connection must not queue behind a writer waiting on our own fence")
	for _, key := range []string{"codefly.principal_id", "codefly.organization_id", "codefly.installation_id", "codefly.installation_revision", "codefly.binding_id"} {
		attrs := held.Attributes()
		attrs[key] = "different"
		raw, err = json.Marshal(attrs)
		require.NoError(t, err)
		require.NoError(t, lockModuleJobAuthority(ctx, executor, raw))
	}
	require.Equal(t, 5, executor.locks, "a different decision cannot borrow a lock")
	raw, err = json.Marshal(held.Attributes())
	require.NoError(t, err)
	require.NoError(t, lockModuleJobAuthority(context.Background(), executor, raw))
	require.Equal(t, 6, executor.locks)
}
