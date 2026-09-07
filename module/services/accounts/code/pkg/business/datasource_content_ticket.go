package business

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"accounts/pkg/datasource/github"

	"github.com/codefly-dev/core/wool"
)

// contentTicketTTL bounds how long a content ticket is honoured. A per-file
// change-set job that omits an oversized blob carries a ticket the document
// store redeems shortly after; the token never leaves accounts, so a short life
// is enough and keeps a leaked ticket from being replayed indefinitely.
const contentTicketTTL = 15 * time.Minute

// maxContentTicketBytes is the hard cap on a blob streamed back through a
// content ticket. Anything larger is refused rather than buffered.
const maxContentTicketBytes = 25 * 1024 * 1024

// ErrContentTicketInvalid reports a malformed, forged, or foreign content
// ticket; ErrContentTicketExpired reports a well-formed ticket past its expiry.
var (
	ErrContentTicketInvalid = errors.New("datasource: content ticket is invalid")
	ErrContentTicketExpired = errors.New("datasource: content ticket has expired")
)

// contentTicketClaims is the signed body of a content ticket. It binds the
// ticket to one source and one blob so it cannot be redirected to fetch another
// source's content, and to an expiry so it is single-window rather than durable.
type contentTicketClaims struct {
	SourceID  string `json:"source_id"`
	BlobSHA   string `json:"blob_sha"`
	ExpiresAt int64  `json:"expires_at"`
}

// datasourceTicketSigner mints and verifies opaque content tickets with an
// HMAC-SHA256 over the claims. Tickets are not stored; verification is a MAC
// check plus an expiry check.
type datasourceTicketSigner struct {
	key []byte
}

// newDatasourceTicketSigner derives a signing key from seed, domain-separated so
// the same internal secret used elsewhere cannot produce colliding tickets.
func newDatasourceTicketSigner(seed []byte) *datasourceTicketSigner {
	sum := sha256.Sum256(append([]byte("datasource-content-ticket\x00"), seed...))
	return &datasourceTicketSigner{key: sum[:]}
}

func (t *datasourceTicketSigner) mint(sourceID, blobSHA string, now time.Time) (string, error) {
	body, err := json.Marshal(contentTicketClaims{
		SourceID:  sourceID,
		BlobSHA:   blobSHA,
		ExpiresAt: now.Add(contentTicketTTL).Unix(),
	})
	if err != nil {
		return "", err
	}
	encodedBody := base64.RawURLEncoding.EncodeToString(body)
	mac := t.sign(encodedBody)
	return encodedBody + "." + mac, nil
}

func (t *datasourceTicketSigner) verify(ticket string, now time.Time) (*contentTicketClaims, error) {
	encodedBody, mac, ok := strings.Cut(ticket, ".")
	if !ok || encodedBody == "" || mac == "" {
		return nil, ErrContentTicketInvalid
	}
	if !hmac.Equal([]byte(mac), []byte(t.sign(encodedBody))) {
		return nil, ErrContentTicketInvalid
	}
	body, err := base64.RawURLEncoding.DecodeString(encodedBody)
	if err != nil {
		return nil, ErrContentTicketInvalid
	}
	var claims contentTicketClaims
	if err := json.Unmarshal(body, &claims); err != nil {
		return nil, ErrContentTicketInvalid
	}
	if claims.SourceID == "" || claims.BlobSHA == "" {
		return nil, ErrContentTicketInvalid
	}
	if now.Unix() >= claims.ExpiresAt {
		return nil, ErrContentTicketExpired
	}
	return &claims, nil
}

func (t *datasourceTicketSigner) sign(encodedBody string) string {
	mac := hmac.New(sha256.New, t.key)
	mac.Write([]byte(encodedBody))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// ResolveContentTicket redeems a content ticket for the referenced blob's bytes.
// The token stays in accounts: the ticket names a source and a blob sha, and the
// blob is re-fetched with that source's decrypted token, up to
// maxContentTicketBytes. A malformed, forged, or foreign ticket is
// ErrContentTicketInvalid; an expired one is ErrContentTicketExpired.
func (s *Service) ResolveContentTicket(ctx context.Context, ticket string) ([]byte, error) {
	w := wool.Get(ctx).In("ResolveContentTicket")
	if s.datasourceTicketSigner == nil || s.datasourceCipher == nil || s.newGitHubClient == nil {
		return nil, w.NewError("datasource connector is not configured")
	}
	claims, err := s.datasourceTicketSigner.verify(ticket, time.Now().UTC())
	if err != nil {
		return nil, err
	}
	source, err := s.store.GetDatasourceSourceByID(ctx, claims.SourceID)
	if err != nil {
		return nil, w.Wrapf(err, "load source")
	}
	if source == nil || source.Provider != DatasourceProviderGitHub {
		return nil, ErrContentTicketInvalid
	}
	token, err := s.datasourceCipher.DecryptSecret(ctx, DatasourceConnectorSecretPurpose(source.ID), source.CredentialSecretRef)
	if err != nil {
		return nil, w.Wrapf(err, "decrypt access token")
	}
	content, err := s.newGitHubClient(token).GetBlob(ctx, source.Repo, claims.BlobSHA, maxContentTicketBytes)
	if err != nil {
		if errors.Is(err, github.ErrFileTooLarge) {
			return nil, w.NewError("blob exceeds the content ticket size limit")
		}
		return nil, w.Wrapf(err, "fetch blob")
	}
	return content, nil
}
