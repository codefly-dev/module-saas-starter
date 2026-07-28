package analytics

import (
	"errors"
	"strings"
	"time"

	analyticsv1 "accounts/pkg/gen/saas/analytics/v1"

	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type NewEventInput struct {
	EventID        string
	Name           string
	OccurredAt     time.Time
	ActorUserID    string
	AnonymousID    string
	OrganizationID string
	SessionID      string
	Source         analyticsv1.EventSource
	Context        *analyticsv1.EventContext
	Properties     map[string]any
	ConsentState   analyticsv1.ConsentState
}

func (r *Registry) NewEvent(input NewEventInput) (*analyticsv1.ProductEvent, error) {
	definition, ok := r.Definition(input.Name)
	if !ok {
		return nil, errors.New("analytics: event is not registered")
	}
	properties, err := structpb.NewStruct(input.Properties)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	occurredAt := input.OccurredAt.UTC()
	if input.OccurredAt.IsZero() {
		occurredAt = now
	}
	consent := input.ConsentState
	if consent == analyticsv1.ConsentState_CONSENT_STATE_UNSPECIFIED &&
		(input.Source != analyticsv1.EventSource_EVENT_SOURCE_WEB ||
			definition.Purpose == analyticsv1.AnalyticsPurpose_ANALYTICS_PURPOSE_ESSENTIAL) {
		consent = analyticsv1.ConsentState_CONSENT_STATE_NOT_REQUIRED
	}
	eventID := input.EventID
	if eventID == "" {
		eventID = uuid.NewString()
	}
	event := &analyticsv1.ProductEvent{
		EventId:        eventID,
		EventName:      definition.Name,
		SchemaVersion:  definition.SchemaVersion,
		OccurredAt:     timestamppb.New(occurredAt),
		ReceivedAt:     timestamppb.New(now),
		ActorUserId:    input.ActorUserID,
		AnonymousId:    input.AnonymousID,
		OrganizationId: input.OrganizationID,
		SessionId:      input.SessionID,
		Source:         input.Source,
		Context:        input.Context,
		Properties:     properties,
		Privacy: &analyticsv1.EventPrivacy{
			Purpose:           definition.Purpose,
			ConsentState:      consent,
			PiiClassification: definition.PIIClassification,
		},
	}
	if err := r.Validate(event); err != nil {
		return nil, err
	}
	return event, nil
}

func DeterministicEventID(eventName string, identityParts ...string) string {
	name := "saas.analytics/" + eventName + "/" + strings.Join(identityParts, "/")
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte(name)).String()
}
