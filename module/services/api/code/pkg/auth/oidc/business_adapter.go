package oidc

import (
	"context"

	"api/pkg/business"
)

// AsBusinessExchanger wraps an *Exchanger so it satisfies the
// business.CodeExchanger interface without creating an import cycle
// between pkg/business and pkg/auth/oidc.
func AsBusinessExchanger(ex *Exchanger) business.CodeExchanger {
	return &businessExchangerAdapter{ex: ex}
}

type businessExchangerAdapter struct{ ex *Exchanger }

func (a *businessExchangerAdapter) Exchange(ctx context.Context, code, redirectURI string) (business.ExchangedTokens, error) {
	resp, err := a.ex.Exchange(ctx, code, redirectURI)
	if err != nil {
		return business.ExchangedTokens{}, err
	}
	return business.ExchangedTokens{
		AccessToken: resp.AccessToken,
		IDToken:     resp.IDToken,
	}, nil
}
