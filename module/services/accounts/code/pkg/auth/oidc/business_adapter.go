package oidc

import (
	"context"

	"accounts/pkg/business"
)

// AsBusinessExchanger wraps an *Exchanger so it satisfies the
// business.CodeExchanger interface without creating an import cycle
// between pkg/business and pkg/auth/oidc.
func AsBusinessExchanger(ex *Exchanger) business.CodeExchanger {
	return &businessExchangerAdapter{ex: ex}
}

type businessExchangerAdapter struct{ ex *Exchanger }

func (a *businessExchangerAdapter) Exchange(ctx context.Context, code, redirectURI, codeVerifier string) (business.ExchangedTokens, error) {
	resp, err := a.ex.Exchange(ctx, code, redirectURI, codeVerifier)
	if err != nil {
		return business.ExchangedTokens{}, err
	}
	return business.ExchangedTokens{
		AccessToken: resp.AccessToken,
		IDToken:     resp.IDToken,
	}, nil
}

// WithClientSecretFrom returns the same exchanger presenting derive(its own
// secret) in place of it — the token-exchange diagnostic's negative control,
// which must differ from the real exchange in nothing else.
func (a *businessExchangerAdapter) WithClientSecretFrom(derive func(configured string) (string, error)) (business.CodeExchanger, error) {
	secret, err := derive(a.ex.clientSecret)
	if err != nil {
		return nil, err
	}
	control := *a.ex
	control.clientSecret = secret
	return &businessExchangerAdapter{ex: &control}, nil
}
