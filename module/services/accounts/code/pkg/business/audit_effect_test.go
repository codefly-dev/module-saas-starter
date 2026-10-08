package business

import (
	"math"
	"testing"
	"time"
)

func TestAuditEffectFingerprint(t *testing.T) {
	original := AuditEntry{OrgID: "tenant", ActorID: "actor", ActorType: ActorTypeUser,
		EventType: "example.changed", Resource: "example", ResourceID: "entry",
		Payload: map[string]any{"a": 1, "b": "value"}}
	fingerprint, err := AuditEffectFingerprint(original)
	if err != nil {
		t.Fatal(err)
	}
	retry := original
	retry.ID, retry.IPAddress, retry.IdempotencyKey = "new-attempt", "new-address", "other-key"
	retry.CreatedAt, retry.SchemaVersion = time.Now(), 2
	retry.Payload = map[string]any{"b": "value", "a": 1}
	same, err := AuditEffectFingerprint(retry)
	if err != nil || fingerprint != same {
		t.Fatal("attempt metadata or map order changed semantic intent")
	}
	for name, change := range map[string]func(*AuditEntry){
		"tenant":        func(e *AuditEntry) { e.OrgID = "other" },
		"actor":         func(e *AuditEntry) { e.ActorID = "other" },
		"actor kind":    func(e *AuditEntry) { e.ActorType = ActorTypeAgent },
		"event":         func(e *AuditEntry) { e.EventType = "example.deleted" },
		"resource":      func(e *AuditEntry) { e.Resource = "other" },
		"entry":         func(e *AuditEntry) { e.ResourceID = "other" },
		"impersonator":  func(e *AuditEntry) { e.ImpersonatedBy = "other" },
		"impersonation": func(e *AuditEntry) { e.IsImpersonated = true },
		"client":        func(e *AuditEntry) { e.ClientID = "other" },
		"payload":       func(e *AuditEntry) { e.Payload = map[string]any{"a": 2, "b": "value"} },
	} {
		t.Run(name, func(t *testing.T) {
			changed := original
			change(&changed)
			got, err := AuditEffectFingerprint(changed)
			if err != nil || got == fingerprint {
				t.Fatalf("changed intent not distinguished: %v", err)
			}
		})
	}
	original.Payload = nil
	empty, err := AuditEffectFingerprint(original)
	if err != nil {
		t.Fatal(err)
	}
	original.Payload = map[string]any{}
	explicitEmpty, err := AuditEffectFingerprint(original)
	if err != nil || empty != explicitEmpty {
		t.Fatal("absent and empty stored payloads differ")
	}
	original.Payload = map[string]any{"unsupported": math.NaN()}
	if _, err := AuditEffectFingerprint(original); err == nil {
		t.Fatal("unsupported payload accepted")
	}
}
