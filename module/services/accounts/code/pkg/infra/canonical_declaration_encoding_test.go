package infra

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/codefly-dev/authority/libraries/authority-core/go/authority"
	"github.com/codefly-dev/authority/libraries/authority-core/go/domain"
	basev1 "github.com/codefly-dev/core/generated/go/codefly/base/v1"
	"google.golang.org/protobuf/proto"
)

func TestCanonicalDeclarationStorageRetainsOwnerValues(t *testing.T) {
	for _, role := range []string{"", "none", "reader"} {
		m := domain.Module{InstallationRole: role, EdgeDependencies: []domain.EdgeDependency{
			{Module: "acme.records", Presence: "required"}, {Module: "acme.annotations", Presence: "optional"},
		}}
		raw, err := encodeModule(m)
		if err != nil {
			t.Fatal(err)
		}
		var got domain.Module
		if err := decodeModule(&got, raw); err != nil {
			t.Fatal(err)
		}
		if got.InstallationRole != role || !reflect.DeepEqual(got.EdgeDependencies, m.EdgeDependencies) {
			t.Fatalf("owner declaration changed in storage: %+v", got)
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(raw, &fields); err != nil {
			t.Fatal(err)
		}
		if role == "" && fields["installation_role"] != nil {
			t.Fatal("absence added a digest-visible field")
		}
	}
	for _, edges := range [][]domain.EdgeDependency{nil, {}} {
		raw, err := encodeModule(domain.Module{EdgeDependencies: edges})
		if err != nil {
			t.Fatal(err)
		}
		if string(raw) != "{}" {
			t.Fatalf("legacy empty declarations changed: %s", raw)
		}
	}
}

// legacyModuleDocument is a stored declaration written before a role's grant
// could be addressed to an audience: it has no audience_roles.
const legacyModuleDocument = `{"resource_kinds":[{"kind":"acme.note","actions":["read","write"]}],"owner_aware":true,` +
	`"bindings":{"records":{"audience":"acme.records","invoke":[{"kind":"acme.records.item","actions":["read"]}]}},` +
	`"has_roles":true,"roles":{"admin":[{"kind":"acme.note","actions":["read","write"]}],"user":[{"kind":"acme.note","actions":["read"]},{"kind":"acme.records.item","actions":["read"]}]}}`

// A role's grant addressed to one audience is stored and read back at that
// audience: a store that dropped it would hold every approved role to its
// literal scopes, and one that moved it would grant it at another module.
func TestCanonicalDeclarationStorageKeepsAudienceRoles(t *testing.T) {
	m := domain.Module{Name: "acme.solution", Roles: map[string][]*basev1.WorkScopeV1{"admin": {{ResourceKind: "acme.note", Actions: []string{"read"}}}, "user": nil},
		AudienceRoles: map[string]map[string][]*basev1.WorkScopeV1{
			"admin": {"acme.records": {{ResourceKind: "acme.records.item", Actions: []string{"read", "write"}}}, "acme.annotations": {{ResourceKind: "acme.annotation", Actions: []string{"read"}}}},
			"user":  {"acme.records": {{ResourceKind: "acme.records.item", Actions: []string{"read"}}}},
		}}
	raw, err := encodeModule(m)
	if err != nil {
		t.Fatal(err)
	}
	got := domain.Module{Name: m.Name}
	if err := decodeModule(&got, raw); err != nil {
		t.Fatal(err)
	}
	if len(got.AudienceRoles) != len(m.AudienceRoles) {
		t.Fatalf("audience roles changed in storage: %v", got.AudienceRoles)
	}
	for role, audiences := range m.AudienceRoles {
		if len(got.AudienceRoles[role]) != len(audiences) {
			t.Fatalf("role %s is addressed to %v, want %v", role, got.AudienceRoles[role], audiences)
		}
		for audience, scopes := range audiences {
			read := got.AudienceRoles[role][audience]
			if len(read) != len(scopes) {
				t.Fatalf("role %s at %s reads %v, want %v", role, audience, read, scopes)
			}
			for i := range scopes {
				if !proto.Equal(read[i], scopes[i]) {
					t.Fatalf("role %s at %s reads %v, want %v", role, audience, read[i], scopes[i])
				}
			}
		}
	}
	// The stored copy shares nothing with the declaration it was read from.
	raw2, err := encodeModule(got)
	if err != nil || string(raw2) != string(raw) {
		t.Fatalf("a stored declaration does not encode to itself: %s, want %s (%v)", raw2, raw, err)
	}
	was, err := authority.OfferDigest(got)
	if err != nil {
		t.Fatal(err)
	}
	got.AudienceRoles["user"]["acme.records"][0].Actions = []string{"read", "write"}
	if now, _ := authority.OfferDigest(got); now == was {
		t.Fatal("a wider audience grant has the digest of the approved offer")
	}
}

// A declaration stored before audience grants reads with none, and digests as
// it did when it was approved: a consent recorded against it names that
// digest (ConsentReceipt.Digest), and a digest that moved by the upgrade
// alone would leave every such installation behind its own approval.
func TestALegacyStoredDeclarationReadsWithNoAudienceRolesAndKeepsItsOfferDigest(t *testing.T) {
	m := domain.Module{Name: "acme.solution", Principal: "acme.solution", Kind: domain.KindSolution, Generation: 3}
	if err := decodeModule(&m, []byte(legacyModuleDocument)); err != nil {
		t.Fatal(err)
	}
	if m.AudienceRoles != nil {
		t.Fatalf("a legacy declaration reads audience roles %v, want nil", m.AudienceRoles)
	}
	if len(m.Roles["user"]) != 2 || m.Bindings["records"].Audience != "acme.records" {
		t.Fatalf("the legacy declaration did not read: %+v", m)
	}
	// The digest is of the declaration's JSON, in which the field is absent:
	// what it was before the field existed.
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	if _, ok := fields["AudienceRoles"]; ok {
		t.Fatal("a declaration with no audience grant digests an AudienceRoles field: every approved digest moves")
	}
	digest, err := authority.OfferDigest(m)
	if err != nil {
		t.Fatal(err)
	}
	// Computed by authority-core at 6c64e549645a, whose domain.Module has no
	// AudienceRoles, over this same declaration.
	const approved = "sha256:c94305aaed211f5a10680d5ddbb861f3ba4613cd36d4550e2f1f33c0022a26e2"
	if digest != approved {
		t.Fatalf("the legacy declaration digests %s, want the digest its consent recorded, %s", digest, approved)
	}
	// Stored again, it is the same bytes' worth: no audience_roles appears.
	again, err := encodeModule(m)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(again, &fields); err != nil {
		t.Fatal(err)
	}
	if _, ok := fields["audience_roles"]; ok {
		t.Fatalf("a legacy declaration is stored with audience_roles: %s", again)
	}
	reread := domain.Module{Name: m.Name, Principal: m.Principal, Kind: m.Kind, Generation: m.Generation}
	if err := decodeModule(&reread, again); err != nil {
		t.Fatal(err)
	}
	if got, _ := authority.OfferDigest(reread); got != approved {
		t.Fatalf("stored again, the legacy declaration digests %s, want %s", got, approved)
	}
}
