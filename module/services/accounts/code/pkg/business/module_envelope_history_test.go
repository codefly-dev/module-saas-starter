package business

import (
	"reflect"
	"testing"
)

func TestPrincipalEnvelopeLifetimeIncludesUnobservedWithdrawal(t *testing.T) {
	records := []*ModuleEnvelopeRecord{
		{Revision: 2, Document: []byte(`{"revision":2,"principals":{"example":{"grant":{"tenant":"11111111-1111-4111-8111-111111111111"}}}}`)},
		{Revision: 5, Document: []byte(`{"revision":5,"principals":{}}`)},
		{Revision: 8, Document: []byte(`{"revision":8,"principals":{"example":{"grant":{"tenant":"11111111-1111-4111-8111-111111111111"}}}}`)},
		{Revision: 9, Document: []byte(`{"revision":9,"principals":{"example":{"grant":{"tenant":"11111111-1111-4111-8111-111111111111"}},"other":{"grant":{"tenant":"22222222-2222-4222-8222-222222222222"}}}}`)},
	}
	want := map[string]uint64{"example": 8, "other": 9}
	for range 2 {
		got, err := principalEnvelopeGenerations(records, 9)
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("restart projection = %v, %v; want %v", got, err, want)
		}
	}
	for _, through := range []uint64{0, 7, 10} {
		if _, err := principalEnvelopeGenerations(records, through); err == nil {
			t.Fatalf("accepted missing snapshot revision %d", through)
		}
	}
	if _, err := principalEnvelopeGenerations(nil, 9); err == nil {
		t.Fatal("accepted missing history")
	}
}
