package business

import "context"

const (
	DelegatedAudienceExchangeIssued  = "issued"
	DelegatedAudienceExchangeRefused = "refused"
)

// DelegatedAudienceExchangeObservation is the attributable, credential-free
// record of one installed audience exchange. Every identity field comes from
// verified Work Contexts; callers must never populate it from request metadata.
type DelegatedAudienceExchangeObservation struct {
	Caller ModuleCaller
	// OwnerID is the person the exchange acts for. Empty only on the
	// reference arm, and only when the grant reference resolved to no
	// delegation at all, so no person was ever identified.
	OwnerID string
	// DelegationID is the grant reference presented, on the arm that presents
	// one. It is what makes a refusal legible when OwnerID cannot be.
	DelegationID string
	Actor        *Principal
	ActorID      string
	Tenant       string
	BindingKind  string
	BindingID    string
	Audience     string
	Lookup       bool
	Outcome      string
	RefusalCode  string
}

// ObserveDelegatedAudienceExchange records an issue or refusal independently
// of any domain transaction. The exchange signs a short-lived capability but
// mutates no durable domain row, while refusals must survive by definition.
func (s *Service) ObserveDelegatedAudienceExchange(ctx context.Context, observation DelegatedAudienceExchangeObservation) {
	payload := map[string]any{
		"actor_principal_id":  observation.ActorID,
		"module_principal_id": observation.Caller.PrincipalID,
		"binding_kind":        observation.BindingKind,
		"binding_id":          observation.BindingID,
		"lookup":              observation.Lookup,
		"outcome":             observation.Outcome,
	}
	if observation.OwnerID != "" {
		payload["owner_principal_id"] = observation.OwnerID
	}
	if observation.DelegationID != "" {
		payload["delegation_id"] = observation.DelegationID
	}
	if observation.Audience != "" {
		payload["audience"] = observation.Audience
	}
	if observation.RefusalCode != "" {
		payload["refusal_code"] = observation.RefusalCode
	}
	actorType := delegatedAudienceActorType(observation.Actor)
	s.emit(ctx, observation.ActorID, actorType, EventDelegatedAudienceExchange,
		"delegated_audience_binding", observation.BindingID, observation.Tenant, payload)
}

func delegatedAudienceActorType(actor *Principal) string {
	if actor == nil {
		return ActorTypeUser
	}
	switch actor.Kind {
	case PrincipalKindAgent:
		return ActorTypeAgent
	case PrincipalKindService:
		return ActorTypeSystem
	default:
		return ActorTypeUser
	}
}
