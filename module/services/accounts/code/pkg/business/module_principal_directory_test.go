package business

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// pagedPrincipalStore answers ListPrincipals with a fixed page, filtered by kind
// the way the real store's kind predicate filters, so a test can say whether
// the stored rows continue past it.
type pagedPrincipalStore struct {
	principalLookupStore
	rows []*Principal
	next string

	// gotPageSize records the budget the business layer actually asked the store
	// for, so the test can verify the page budget reaches the store unchanged.
	gotPageSize int32
}

func (s *pagedPrincipalStore) ListPrincipals(_ context.Context, _, kind string, pageSize int32, _ string) ([]*Principal, string, error) {
	s.gotPageSize = pageSize
	var out []*Principal
	for _, p := range s.rows {
		if kind == "" || p.Kind == kind {
			out = append(out, p)
		}
	}
	return out, s.next, nil
}

// A deployment that spells a module's tenant in any form uuid.Parse accepts gets
// the canonical form back. Every downstream comparison — the directory, the
// operation-context revision check, the bound org a Work Context seals — is a
// string compare against the lowercase id Postgres returns, so a non-canonical
// spelling would pass validation and then match its own organization nowhere.
func TestParseModulePrincipalRegistry_CanonicalizesTheTenant(t *testing.T) {
	const canonical = "019f6bf7-6a01-7001-8001-0000000000c1"
	for _, spelling := range []string{
		canonical,
		"019F6BF7-6A01-7001-8001-0000000000C1",
		"{019f6bf7-6a01-7001-8001-0000000000c1}",
		"urn:uuid:019f6bf7-6a01-7001-8001-0000000000c1",
		"019f6bf76a01700180010000000000c1",
	} {
		t.Run(spelling, func(t *testing.T) {
			registry, err := ParseModulePrincipalRegistry(`{"documents": {"tenant": "` + spelling + `"}}`)
			require.NoError(t, err)
			require.Equal(t, canonical, registry[ModulePrincipalID("documents")].Tenant)
		})
	}
}
