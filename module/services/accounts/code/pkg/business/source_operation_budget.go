package business

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/url"
	"time"

	"accounts/pkg/datasource/connector"
)

// apiCredentialBudgetKey groups new API sources using the same provider
// credential without storing a plaintext fingerprint. It is fixed at connect,
// so access-token caching and refresh-token rotation never reset the meter.
func (s *Service) apiCredentialBudgetKey(cfg *APIDatasourceConfig, credential string) string {
	if cfg == nil || len(s.datasourceLinkKey) == 0 {
		return ""
	}
	origin, err := url.Parse(cfg.BaseURL)
	if err != nil {
		return ""
	}
	identity := []string{origin.Scheme, origin.Host, cfg.CredentialKind, credential}
	if cfg.OAuth2 != nil {
		identity = append(identity, cfg.OAuth2.TokenURL, cfg.OAuth2.ClientID)
		if cfg.OAuth2.Grant == OAuth2AuthorizationCode {
			// The same person's authorizations under one deployment app spend one
			// provider quota even if they linked several source records.
			identity[3] = cfg.PersonalOwnerUserID
		}
	}
	encoded, _ := json.Marshal(identity)
	mac := hmac.New(sha256.New, s.datasourceLinkKey)
	_, _ = mac.Write([]byte("datasource-api-budget\x00"))
	_, _ = mac.Write(encoded)
	return "api:credential:" + hex.EncodeToString(mac.Sum(nil))
}

var apiOperationBudget = connector.Budget{OperationsPerWindow: 60, Window: time.Minute, BackgroundSharePercent: 80}
