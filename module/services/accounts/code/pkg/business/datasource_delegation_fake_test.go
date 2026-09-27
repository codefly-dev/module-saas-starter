package business_test

import (
	"context"

	"accounts/pkg/business"
)

// The datasource fake holds no source delegations, and its registry declares no
// module binding that accepts one, so a connect records none. The event paths
// that end a delegation (a source deleted) still revoke unconditionally; here
// they find nothing to revoke. source_delegation_test.go exercises the model.
func (f *datasourceFakeStore) RevokeSourceDelegations(
	context.Context, business.SourceDelegationFilter, string, string,
) ([]*business.SourceDelegation, error) {
	return nil, nil
}
