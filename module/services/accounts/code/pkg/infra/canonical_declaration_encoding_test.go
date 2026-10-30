package infra

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/codefly-dev/authority/libraries/authority-core/go/domain"
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
