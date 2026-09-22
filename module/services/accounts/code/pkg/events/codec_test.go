package events_test

import (
	"encoding/json"
	"testing"
	"time"

	"accounts/pkg/events"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func fullEnvelope() *events.EventEnvelope {
	return &events.EventEnvelope{
		Id:               uuid.NewString(),
		Type:             "documents.entry.ingested",
		Source:           "urn:codefly:documents/ingest",
		Subject:          "entry-42",
		Time:             timestamppb.New(time.Date(2026, 9, 7, 12, 0, 0, 123456789, time.UTC)),
		Specversion:      "1.0",
		Datacontenttype:  "application/protobuf",
		Dataschema:       "codefly/events/documents.entry.ingested@1",
		Data:             []byte{0x00, 0x01, 0x02, 0xff, 0xfe},
		TenantId:         uuid.NewString(),
		BoundaryId:       "scope-node-9",
		PartitionKey:     "tenant/source",
		CorrelationId:    "corr-1",
		CausationId:      "cause-1",
		ActorPrincipalId: "actor-1",
		OwnerPrincipalId: "owner-1",
		Traceparent:      "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01",
		SchemaVersion:    7,
	}
}

func TestCloudEventJSONRoundTripsByteForByte(t *testing.T) {
	original := fullEnvelope()

	encoded, err := events.MarshalCloudEventJSON(original)
	require.NoError(t, err)

	decoded, err := events.UnmarshalCloudEventJSON(encoded)
	require.NoError(t, err)
	require.True(t, proto.Equal(original, decoded), "CloudEvents JSON binding must round-trip the envelope exactly")
}

func TestCloudEventJSONEmitsEveryExtension(t *testing.T) {
	original := fullEnvelope()
	encoded, err := events.MarshalCloudEventJSON(original)
	require.NoError(t, err)

	var document map[string]any
	require.NoError(t, json.Unmarshal(encoded, &document))

	require.Equal(t, original.GetTenantId(), document["tenantid"])
	require.Equal(t, original.GetBoundaryId(), document["boundaryid"])
	require.Equal(t, original.GetPartitionKey(), document["partitionkey"])
	require.Equal(t, original.GetCorrelationId(), document["correlationid"])
	require.Equal(t, original.GetCausationId(), document["causationid"])
	require.Equal(t, original.GetActorPrincipalId(), document["actorprincipalid"])
	require.Equal(t, original.GetOwnerPrincipalId(), document["ownerprincipalid"])
	require.Equal(t, original.GetTraceparent(), document["traceparent"])
	require.Equal(t, "7", document["schemaversion"], "extensions are string-valued on the wire")
	require.Equal(t, "1.0", document["specversion"])
}

func TestProtoRoundTrips(t *testing.T) {
	original := fullEnvelope()
	encoded, err := events.MarshalProto(original)
	require.NoError(t, err)
	decoded, err := events.UnmarshalProto(encoded)
	require.NoError(t, err)
	require.True(t, proto.Equal(original, decoded))
}

func TestCloudEventJSONRoundTripsWithoutExtensions(t *testing.T) {
	original := &events.EventEnvelope{
		Id:          uuid.NewString(),
		Type:        "documents.entry.deleted",
		Source:      "urn:codefly:documents/ingest",
		Specversion: "1.0",
	}
	encoded, err := events.MarshalCloudEventJSON(original)
	require.NoError(t, err)
	decoded, err := events.UnmarshalCloudEventJSON(encoded)
	require.NoError(t, err)
	require.True(t, proto.Equal(original, decoded))
}
