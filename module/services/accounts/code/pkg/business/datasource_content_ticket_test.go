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

// TestAttack_UnsetTicketKeyDisablesTickets: an unset key must leave content tickets
// disabled, which minting and redemption already treat as unavailable, and must NOT
// fall back to any other value — SP-SEC-07 requires each purpose's key to be its
// own, derived from no credential shared beyond its owner.
func TestAttack_UnsetTicketKeyDisablesTickets(t *testing.T) {
	for _, key := range [][]byte{nil, {}} {
		s := &Service{}
		// A link key is present, so an absent ticket key cannot be satisfied by the
		// other purpose's value.
		s.SetDatasourceKeys(key, []byte("a-delivered-link-key"))
		if s.datasourceTicketSigner != nil {
			t.Fatalf("SetDatasourceKeys(%q, ...) installed a signer keyed by a public constant", key)
		}
	}
}

// And the other way round: an absent account-link key leaves the link state
// unsignable rather than borrowing the ticket key.
func TestAttack_UnsetAccountLinkKeyDisablesLinkState(t *testing.T) {
	for _, key := range [][]byte{nil, {}} {
		s := &Service{}
		s.SetDatasourceKeys([]byte("a-delivered-ticket-key"), key)
		if s.datasourceLinkKey != nil {
			t.Fatalf("SetDatasourceKeys(..., %q) installed a link key from somewhere else", key)
		}
	}
}

// The two purposes do not share a key even when an operator provisions one value
// for both: the link key is domain-separated, so a content ticket and a link state
// can never pass for each other.
func TestDatasourceKeysStayDistinctUnderOneProvisionedValue(t *testing.T) {
	s := &Service{}
	one := []byte("one-value-provisioned-for-both-purposes")
	s.SetDatasourceKeys(one, one)
	if s.datasourceTicketSigner == nil || s.datasourceLinkKey == nil {
		t.Fatal("both purposes must be available")
	}
	if string(s.datasourceLinkKey) == string(s.datasourceTicketSigner.key) {
		t.Fatal("the link key and the ticket key must differ even from one provisioned value")
	}
}

// Each purpose's key must derive from ITS OWN delivered input. Asserting only that the
// two keys differ is not enough: a link key derived from the TICKET input through a
// domain label still differs from the ticket key, so that assertion passes while the
// two purposes once again share one secret — which is the whole of SA-F-KEYREUSE.
//
// So this varies one input at a time and requires the other purpose not to move.
func TestR1019DatasourceKeysDeriveFromTheirOwnInput(t *testing.T) {
	keys := func(ticket, link string) (string, string) {
		s := &Service{}
		s.SetDatasourceKeys([]byte(ticket), []byte(link))
		if s.datasourceTicketSigner == nil || s.datasourceLinkKey == nil {
			t.Fatal("both purposes must be available")
		}
		return string(s.datasourceTicketSigner.key), string(s.datasourceLinkKey)
	}

	baseTicket, baseLink := keys("ticket-key-one", "link-key-one")

	// Changing the LINK input must move the link key and leave the ticket key alone.
	movedTicket, movedLink := keys("ticket-key-one", "link-key-two")
	if movedLink == baseLink {
		t.Fatal("the link key does not depend on the account-link input")
	}
	if movedTicket != baseTicket {
		t.Fatal("the ticket key must not depend on the account-link input")
	}

	// And changing the TICKET input must move the ticket key and leave the link key
	// alone. This is the direction that catches a link key derived from the ticket
	// input.
	movedTicket, movedLink = keys("ticket-key-two", "link-key-one")
	if movedTicket == baseTicket {
		t.Fatal("the ticket key does not depend on the content-ticket input")
	}
	if movedLink != baseLink {
		t.Fatal("the link key must not depend on the content-ticket input")
	}
}
