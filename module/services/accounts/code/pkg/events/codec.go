package events

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// MarshalProto renders an envelope as deterministic protobuf. The wire form is
// stable across processes so it can back an idempotency fingerprint.
func MarshalProto(e *EventEnvelope) ([]byte, error) {
	return (proto.MarshalOptions{Deterministic: true}).Marshal(e)
}

// UnmarshalProto parses the protobuf wire form into a fresh envelope.
func UnmarshalProto(data []byte) (*EventEnvelope, error) {
	e := &EventEnvelope{}
	if err := proto.Unmarshal(data, e); err != nil {
		return nil, fmt.Errorf("events: unmarshal protobuf envelope: %w", err)
	}
	return e, nil
}

// cloudEventExtension pairs one envelope extension attribute with its
// CloudEvents attribute name. CloudEvents extension names are lowercase
// alphanumerics, so the proto snake_case field names map to the compact forms
// here; the mapping is 1:1 and total so the JSON binding round-trips.
type cloudEventExtension struct {
	name string
	get  func(*EventEnvelope) string
	set  func(*EventEnvelope, string) error
}

func stringExtension(name string, get func(*EventEnvelope) string, set func(*EventEnvelope, string)) cloudEventExtension {
	return cloudEventExtension{name: name, get: get, set: func(e *EventEnvelope, v string) error { set(e, v); return nil }}
}

var cloudEventExtensions = []cloudEventExtension{
	stringExtension("tenantid", (*EventEnvelope).GetTenantId, func(e *EventEnvelope, v string) { e.TenantId = v }),
	stringExtension("boundaryid", (*EventEnvelope).GetBoundaryId, func(e *EventEnvelope, v string) { e.BoundaryId = v }),
	stringExtension("partitionkey", (*EventEnvelope).GetPartitionKey, func(e *EventEnvelope, v string) { e.PartitionKey = v }),
	stringExtension("correlationid", (*EventEnvelope).GetCorrelationId, func(e *EventEnvelope, v string) { e.CorrelationId = v }),
	stringExtension("causationid", (*EventEnvelope).GetCausationId, func(e *EventEnvelope, v string) { e.CausationId = v }),
	stringExtension("actorprincipalid", (*EventEnvelope).GetActorPrincipalId, func(e *EventEnvelope, v string) { e.ActorPrincipalId = v }),
	stringExtension("ownerprincipalid", (*EventEnvelope).GetOwnerPrincipalId, func(e *EventEnvelope, v string) { e.OwnerPrincipalId = v }),
	stringExtension("traceparent", (*EventEnvelope).GetTraceparent, func(e *EventEnvelope, v string) { e.Traceparent = v }),
	{
		name: "schemaversion",
		get: func(e *EventEnvelope) string {
			if e.GetSchemaVersion() == 0 {
				return ""
			}
			return strconv.FormatUint(uint64(e.GetSchemaVersion()), 10)
		},
		set: func(e *EventEnvelope, v string) error {
			parsed, err := strconv.ParseUint(v, 10, 32)
			if err != nil {
				return fmt.Errorf("events: parse schemaversion extension: %w", err)
			}
			e.SchemaVersion = uint32(parsed)
			return nil
		},
	},
}

// MarshalCloudEventJSON renders an envelope in the CloudEvents 1.0 structured
// JSON binding. The payload is opaque bytes, so it is always carried in
// data_base64; every extension attribute is emitted under its CloudEvents name.
func MarshalCloudEventJSON(e *EventEnvelope) ([]byte, error) {
	document := map[string]any{}
	putIfSet(document, "specversion", e.GetSpecversion())
	putIfSet(document, "id", e.GetId())
	putIfSet(document, "source", e.GetSource())
	putIfSet(document, "type", e.GetType())
	putIfSet(document, "subject", e.GetSubject())
	putIfSet(document, "datacontenttype", e.GetDatacontenttype())
	putIfSet(document, "dataschema", e.GetDataschema())
	if e.GetTime() != nil {
		document["time"] = e.GetTime().AsTime().UTC().Format(time.RFC3339Nano)
	}
	if len(e.GetData()) > 0 {
		document["data_base64"] = base64.StdEncoding.EncodeToString(e.GetData())
	}
	for _, extension := range cloudEventExtensions {
		putIfSet(document, extension.name, extension.get(e))
	}
	body, err := json.Marshal(document)
	if err != nil {
		return nil, fmt.Errorf("events: marshal CloudEvents JSON: %w", err)
	}
	return body, nil
}

// UnmarshalCloudEventJSON parses the CloudEvents 1.0 structured JSON binding.
func UnmarshalCloudEventJSON(data []byte) (*EventEnvelope, error) {
	var document map[string]string
	if err := json.Unmarshal(data, &document); err != nil {
		return nil, fmt.Errorf("events: unmarshal CloudEvents JSON: %w", err)
	}
	e := &EventEnvelope{
		Specversion:     document["specversion"],
		Id:              document["id"],
		Source:          document["source"],
		Type:            document["type"],
		Subject:         document["subject"],
		Datacontenttype: document["datacontenttype"],
		Dataschema:      document["dataschema"],
	}
	if raw, ok := document["time"]; ok {
		parsed, err := time.Parse(time.RFC3339Nano, raw)
		if err != nil {
			return nil, fmt.Errorf("events: parse CloudEvents time: %w", err)
		}
		e.Time = timestamppb.New(parsed)
	}
	if raw, ok := document["data_base64"]; ok {
		decoded, err := base64.StdEncoding.DecodeString(raw)
		if err != nil {
			return nil, fmt.Errorf("events: decode CloudEvents data_base64: %w", err)
		}
		e.Data = decoded
	}
	for _, extension := range cloudEventExtensions {
		if raw, ok := document[extension.name]; ok {
			if err := extension.set(e, raw); err != nil {
				return nil, err
			}
		}
	}
	return e, nil
}

func putIfSet(document map[string]any, key, value string) {
	if value != "" {
		document[key] = value
	}
}
