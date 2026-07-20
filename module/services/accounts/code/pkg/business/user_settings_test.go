package business_test

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	"accounts/pkg/business"
)

// settingsFakeStore — partial fake covering GetUserSettings /
// UpdateUserSettings. Mimics the postgres `settings || $2::jsonb`
// shallow-merge: top-level keys in patch overwrite stored.
type settingsFakeStore struct {
	business.Store
	mu       sync.Mutex
	settings map[string][]byte
}

type settingsFakeScoped struct {
	business.Scoped
	identity business.Identity
}

func (s *settingsFakeScoped) Within(ctx context.Context, fn func(context.Context) error) error {
	return fn(ctx)
}

func (s *settingsFakeScoped) Identity() business.Identity { return s.identity }

func newSettingsFakeStore() *settingsFakeStore {
	return &settingsFakeStore{settings: map[string][]byte{}}
}

func (f *settingsFakeStore) GetUserSettings(_ context.Context, userID string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if v, ok := f.settings[userID]; ok {
		return v, nil
	}
	return []byte("{}"), nil
}

func (f *settingsFakeStore) UpdateUserSettings(_ context.Context, userID string, patch []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	// Mimic the postgres `||` operator: shallow merge on top-level
	// keys. Test-grade — uses a tiny manual JSON merger so we don't
	// pull in a deps. Good enough for asserting the contract.
	prev := f.settings[userID]
	if len(prev) == 0 {
		prev = []byte("{}")
	}
	merged := mergeJSONShallow(prev, patch)
	f.settings[userID] = merged
	return nil
}

func (f *settingsFakeStore) As(identity business.Identity) business.Scoped {
	return &settingsFakeScoped{identity: identity}
}

// mergeJSONShallow merges b's top-level keys onto a. b's values win
// per key. Both must be valid JSON objects.
func mergeJSONShallow(a, b []byte) []byte {
	if len(b) == 0 || string(b) == "{}" {
		return a
	}
	// Parse both into maps, merge, re-encode. Keeping it dependency-
	// free intentionally — test-only.
	var am, bm map[string]any
	_ = json.Unmarshal(a, &am)
	_ = json.Unmarshal(b, &bm)
	if am == nil {
		am = map[string]any{}
	}
	for k, v := range bm {
		am[k] = v
	}
	out, _ := json.Marshal(am)
	return out
}

// TestGetUserSettings_EmptyRow — fresh users have settings = '{}'.
// The business layer must turn that into a populated zero-value
// struct (not nil) so callers don't have to nil-check.
func TestGetUserSettings_EmptyRow(t *testing.T) {
	store := newSettingsFakeStore()
	svc := newSettingsService(store)

	got, err := svc.GetUserSettings(context.Background(), "user-1")
	if err != nil {
		t.Fatalf("GetUserSettings error: %v", err)
	}
	if got == nil {
		t.Fatal("expected non-nil empty struct, got nil")
	}
	if got.Theme != nil || got.Locale != nil {
		t.Errorf("expected all-nil fields on empty row, got theme=%v locale=%v",
			got.Theme, got.Locale)
	}
}

// TestUpdateUserSettings_PartialPatchPreservesUnsetKeys — submitting
// only `theme` must NOT clobber a stored `locale`. This is the
// concatenation-merge contract the FE depends on; without it, every
// FE save would have to round-trip the entire blob and risk lost
// updates.
func TestUpdateUserSettings_PartialPatchPreservesUnsetKeys(t *testing.T) {
	store := newSettingsFakeStore()
	svc := newSettingsService(store)
	ctx := context.Background()

	// Seed: user picked locale=fr earlier.
	locale := "fr"
	if _, err := svc.UpdateUserSettings(ctx, "user-1", &business.UserSettings{Locale: &locale}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	// Now apply: theme=dark only.
	theme := business.ThemePreferenceDark
	got, err := svc.UpdateUserSettings(ctx, "user-1", &business.UserSettings{Theme: &theme})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if got.Theme == nil || *got.Theme != business.ThemePreferenceDark {
		t.Errorf("theme: got %v, want dark", got.Theme)
	}
	if got.Locale == nil || *got.Locale != "fr" {
		t.Errorf("locale should be preserved, got %v", got.Locale)
	}
}

func TestUpdateUserSettingsRejectsInvalidTheme(t *testing.T) {
	store := newSettingsFakeStore()
	svc := newSettingsService(store)
	invalid := business.ThemePreference("sepia")

	if _, err := svc.UpdateUserSettings(context.Background(), "user-1", &business.UserSettings{
		Theme: &invalid,
	}); err == nil {
		t.Fatal("expected invalid theme to be rejected")
	}
}

// TestUpdateUserSettings_NestedReplacesEntireObject — when the FE
// sends `email: { product: false }` the api replaces the WHOLE
// email object (not deep-merge). The FE accommodates by always
// sending the full nested object on any nested-key change; this
// test pins that contract so a future "let's add deep merge"
// refactor doesn't silently change the semantics.
func TestUpdateUserSettings_NestedReplacesEntireObject(t *testing.T) {
	store := newSettingsFakeStore()
	svc := newSettingsService(store)
	ctx := context.Background()

	// Seed: full email block with both true.
	tt := true
	if _, err := svc.UpdateUserSettings(ctx, "user-1", &business.UserSettings{
		Email: &business.EmailSettings{Product: &tt, Marketing: &tt},
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	// Now patch: send only product=false in the email block.
	ff := false
	got, err := svc.UpdateUserSettings(ctx, "user-1", &business.UserSettings{
		Email: &business.EmailSettings{Product: &ff},
	})
	if err != nil {
		t.Fatalf("patch: %v", err)
	}
	if got.Email == nil {
		t.Fatal("expected email block, got nil")
	}
	if got.Email.Product == nil || *got.Email.Product != false {
		t.Errorf("product: got %v, want false", got.Email.Product)
	}
	// Marketing was nil in the patch → on shallow merge of nested
	// objects, it's gone. Documenting the contract.
	if got.Email.Marketing != nil {
		t.Errorf("marketing should be cleared by nested replace, got %v", got.Email.Marketing)
	}
}

// helpers — pulled from std encoding/json indirectly to avoid
// repeated direct imports across the test file.
func newSettingsService(store business.Store) *business.Service {
	svc, _ := business.NewService(store)
	return svc
}
