package business

import (
	"errors"
	"testing"
	"time"
)

func TestContentTicketMintVerifyRoundTrip(t *testing.T) {
	signer := newDatasourceTicketSigner([]byte("internal-key"))
	now := time.Unix(1_700_000_000, 0).UTC()

	ticket, err := signer.mint("source-1", "blob-abc", now)
	if err != nil {
		t.Fatal(err)
	}
	claims, err := signer.verify(ticket, now.Add(time.Minute))
	if err != nil {
		t.Fatalf("verify = %v, want ok", err)
	}
	if claims.SourceID != "source-1" || claims.BlobSHA != "blob-abc" {
		t.Fatalf("claims = %+v", claims)
	}
}

func TestContentTicketExpires(t *testing.T) {
	signer := newDatasourceTicketSigner([]byte("internal-key"))
	now := time.Unix(1_700_000_000, 0).UTC()

	ticket, err := signer.mint("source-1", "blob-abc", now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := signer.verify(ticket, now.Add(contentTicketTTL+time.Second)); !errors.Is(err, ErrContentTicketExpired) {
		t.Fatalf("expired verify = %v, want ErrContentTicketExpired", err)
	}
}

func TestContentTicketRejectsTamperAndForeignKey(t *testing.T) {
	signer := newDatasourceTicketSigner([]byte("internal-key"))
	now := time.Unix(1_700_000_000, 0).UTC()
	ticket, err := signer.mint("source-1", "blob-abc", now)
	if err != nil {
		t.Fatal(err)
	}

	// A ticket minted under a different key must not verify: a forged ticket
	// cannot redeem another deployment's blobs.
	foreign := newDatasourceTicketSigner([]byte("other-key"))
	if _, err := foreign.verify(ticket, now); !errors.Is(err, ErrContentTicketInvalid) {
		t.Fatalf("foreign-key verify = %v, want ErrContentTicketInvalid", err)
	}

	for _, bad := range []string{"", "no-dot", ticket + "x", "." + ticket} {
		if _, err := signer.verify(bad, now); !errors.Is(err, ErrContentTicketInvalid) {
			t.Fatalf("verify(%q) = %v, want ErrContentTicketInvalid", bad, err)
		}
	}
}
