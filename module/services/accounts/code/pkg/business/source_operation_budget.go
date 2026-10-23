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
	return s.credentialBudgetKey("api", identity)
}

// uploadCredentialBudgetKey groups object-storage sources that read with the
// same access key at the same store, so every source on one credential spends
// one quota and honours one provider throttle, as the scheduler's contract
// says (connector/budget.go). Fixed at connect, like an API source's.
func (s *Service) uploadCredentialBudgetKey(cfg *UploadDatasourceConfig) string {
	if cfg == nil || len(s.datasourceLinkKey) == 0 {
		return ""
	}
	origin, err := url.Parse(cfg.Endpoint)
	if err != nil {
		return ""
	}
	return s.credentialBudgetKey("upload", []string{origin.Scheme, origin.Host, cfg.AccessKeyID})
}

// credentialBudgetKey names one provider credential without storing a
// plaintext fingerprint of it: a keyed digest, domain-separated per provider.
func (s *Service) credentialBudgetKey(provider string, identity []string) string {
	encoded, _ := json.Marshal(identity)
	mac := hmac.New(sha256.New, s.datasourceLinkKey)
	_, _ = mac.Write([]byte("datasource-" + provider + "-budget\x00"))
	_, _ = mac.Write(encoded)
	return provider + ":credential:" + hex.EncodeToString(mac.Sum(nil))
}

var apiOperationBudget = connector.Budget{OperationsPerWindow: 60, Window: time.Minute, BackgroundSharePercent: 80}
