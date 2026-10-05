package business

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestListPrincipals_DoesNotProjectDeclaredAuthority(t *testing.T) {
	const org = "019f6bf7-6a01-7001-8001-0000000000c1"
	registry, err := ParseModulePrincipalRegistry(`{
		"example": {"workload":{"service_account":"module","namespace":"acme-prod","container":"app"},"tenant":"` + org + `"},
		"worker": {"workload":{"service_account":"module","namespace":"acme-prod","container":"app"},"tenant":"019f6bf7-6a01-7001-8001-0000000000c2","cross_tenant":true}
	}`)
	require.NoError(t, err)
	stored := &Principal{ID: "019f6bf7-6a01-7001-8001-0000000000a1", Kind: PrincipalKindService, OrgID: org, DisplayName: "Example service"}
	for _, kind := range []string{"", PrincipalKindService, PrincipalKindHuman} {
		for _, cursor := range []string{"", "next-page"} {
			t.Run(kind+"/"+cursor, func(t *testing.T) {
				store := &pagedPrincipalStore{rows: []*Principal{stored}, next: "more"}
				svc, err := NewService(store)
				require.NoError(t, err)
				svc.SetModulePrincipals(registry)
				rows, next, err := svc.ListPrincipals(context.Background(), org, kind, 1, cursor)
				require.NoError(t, err)
				if kind == PrincipalKindHuman {
					require.Empty(t, rows)
				} else {
					require.Equal(t, []*Principal{stored}, rows, "only durable principals belong in the directory")
				}
				require.Equal(t, "more", next)
				require.Equal(t, int32(1), store.gotPageSize, "the store receives the entire page budget")
			})
		}
	}
}
