package business

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"

	"accounts/pkg/auth"
	gen "accounts/pkg/gen/saas/accounts/v1"
)

// signOutMinter is the minter as a sign-out meets it: refresh tokens that name
// sessions, and the access-revocation markers the perimeter reads. A bearer is
// refused once a marker is written under its session id or its family's, which
// is VerifyAccess's rule (pkg/auth/ed25519).
type signOutMinter struct {
	auth.JWTMinter
	sessions map[string]auth.RefreshSession
	families map[string]string
	markers  []string
	failOn   string
	lookup   error
}

func (m *signOutMinter) RefreshSession(_ context.Context, token string) (auth.RefreshSession, error) {
	if m.lookup != nil {
		return auth.RefreshSession{}, m.lookup
	}
	session, ok := m.sessions[token]
	if !ok {
		return auth.RefreshSession{}, auth.ErrRefreshRevoked
	}
	return session, nil
}

func (m *signOutMinter) Revoke(ctx context.Context, token string) error {
	family, ok := m.families[token]
	if !ok {
		return nil
	}
	return m.RevokeSessionAccess(ctx, family)
}

func (m *signOutMinter) RevokeSessionAccess(_ context.Context, key string) error {
	if key == m.failOn {
		return errors.New("marker store unavailable")
	}
	m.markers = append(m.markers, key)
	return nil
}

func (m *signOutMinter) refuses(bearer RevokedSession) bool {
	return slices.Contains(m.markers, bearer.ID) || slices.Contains(m.markers, bearer.FamilyID)
}

// signOutStore holds the open sessions as EndSessionTree reads them: a session
// ends with its family and with what was opened from it, once.
type signOutStore struct {
	Store
	trees map[string][]RevokedSession
	asked []string
}

func (s *signOutStore) WithControlPlane(ctx context.Context, fn func(context.Context) error) error {
	return fn(ctx)
}
func (s *signOutStore) SessionEndsAt(context.Context, string) (time.Time, bool, error) {
	return time.Time{}, false, nil
}
func (s *signOutStore) EndSessionTree(_ context.Context, sessionID, _ string) ([]RevokedSession, error) {
	s.asked = append(s.asked, sessionID)
	tree := s.trees[sessionID]
	delete(s.trees, sessionID)
	return tree, nil
}

// One signed-in session, and a cast opened from it: a session and a family of
// its own, which the refresh family's revocation never reaches.
func signedInWithACast() (*Service, *signOutMinter, *signOutStore, RevokedSession, RevokedSession) {
	session := auth.RefreshSession{ID: uuid.Must(uuid.NewV7()), UserID: uuid.Must(uuid.NewV7()), OrgID: uuid.Must(uuid.NewV7())}
	own := RevokedSession{ID: session.ID.String(), FamilyID: uuid.NewString()}
	cast := RevokedSession{ID: uuid.NewString(), FamilyID: uuid.NewString()}
	minter := &signOutMinter{
		sessions: map[string]auth.RefreshSession{"refresh": session},
		families: map[string]string{"refresh": own.FamilyID},
	}
	store := &signOutStore{trees: map[string][]RevokedSession{own.ID: {own, cast}}}
	return &Service{store: store, minter: minter}, minter, store, own, cast
}

// The browser opening the sign-out address holds its refresh cookie and no
// bearer yet. That sign-out ends the session the cookie names, as End does: the
// access tokens already issued for it are refused, and so are those of the cast
// opened from it.
func TestLogoutWithOnlyTheRefreshCredentialEndsItsSessionAndWhatWasOpenedFromIt(t *testing.T) {
	service, minter, store, own, cast := signedInWithACast()
	if minter.refuses(own) || minter.refuses(cast) {
		t.Fatal("a bearer is refused before anything was signed out")
	}

	if err := service.Logout(context.Background(), &gen.LogoutRequest{RefreshToken: "refresh"}, "", ""); err != nil {
		t.Fatal(err)
	}

	if !slices.Equal(store.asked, []string{own.ID}) {
		t.Fatalf("the session the refresh credential names was not the one ended: %v", store.asked)
	}
	if !minter.refuses(own) {
		t.Fatal("an access token issued before the sign-out is still accepted")
	}
	if !minter.refuses(cast) {
		t.Fatal("the cast opened from the session outlived its sign-out")
	}

	// Signing out again finds nothing open, and says the same.
	if err := service.Logout(context.Background(), &gen.LogoutRequest{RefreshToken: "refresh"}, "", ""); err != nil {
		t.Fatalf("a repeated sign-out is no error: %v", err)
	}
}

// With no credential, and with a refresh token that was never a session's, a
// sign-out ends nothing, writes nothing, and answers both alike: it tells
// nobody whether a token names a session.
func TestLogoutWithNoSessionToNameEndsNothingAndSaysNothing(t *testing.T) {
	for _, token := range []string{"", "never-issued"} {
		service, minter, store, own, cast := signedInWithACast()

		if err := service.Logout(context.Background(), &gen.LogoutRequest{RefreshToken: token}, "", ""); err != nil {
			t.Fatalf("token %q: %v", token, err)
		}

		if len(store.asked) != 0 || len(minter.markers) != 0 {
			t.Fatalf("token %q ended %v and wrote %v", token, store.asked, minter.markers)
		}
		if minter.refuses(own) || minter.refuses(cast) {
			t.Fatalf("token %q signed somebody out", token)
		}
	}
}

// A marker that cannot be written leaves a bearer accepted, so the sign-out is
// reported incomplete; the rest is still done and kept.
func TestLogoutThatCannotEndWhatWasOpenedFromTheSessionIsIncomplete(t *testing.T) {
	service, minter, _, own, cast := signedInWithACast()
	minter.failOn = cast.ID

	err := service.Logout(context.Background(), &gen.LogoutRequest{RefreshToken: "refresh"}, "", "")

	if !errors.Is(err, ErrLogoutIncomplete) {
		t.Fatalf("a cast left accepted was acknowledged as a sign-out: %v", err)
	}
	if !minter.refuses(own) {
		t.Fatal("the failure discarded the revocations that held")
	}
}

// A session that cannot be read is not a session that is absent: the failure
// reaches the caller, after the family revocation was still attempted.
func TestLogoutReportsASessionItCouldNotRead(t *testing.T) {
	service, minter, store, own, _ := signedInWithACast()
	minter.lookup = errors.New("session store unavailable")

	err := service.Logout(context.Background(), &gen.LogoutRequest{RefreshToken: "refresh"}, "", "")

	if !errors.Is(err, minter.lookup) || errors.Is(err, ErrLogoutIncomplete) {
		t.Fatalf("the lookup failure was lost or renamed: %v", err)
	}
	if len(store.asked) != 0 {
		t.Fatal("a session was ended without being named")
	}
	if !slices.Contains(minter.markers, own.FamilyID) {
		t.Fatal("the family revocation was skipped")
	}
}
