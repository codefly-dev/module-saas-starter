package infra_test

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"accounts/pkg/billing"
	billingv1 "accounts/pkg/gen/saas/billing/v1"
	"accounts/pkg/infra"
	"accounts/pkg/jobs"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

type stripeJobRecorder struct {
	event *billingv1.StripeWebhookJob
}

func (recorder *stripeJobRecorder) ProcessWebhook(
	_ context.Context,
	event *billingv1.StripeWebhookJob,
) error {
	recorder.event = proto.Clone(event).(*billingv1.StripeWebhookJob)
	return nil
}

func TestStripeWebhookRunsThroughGenericPostgresJobLifecycle(t *testing.T) {
	pool, err := infra.NewJobWorkerPool(testCtx)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	store := infra.NewPostgresJobStore(pool)

	recorder := &stripeJobRecorder{}
	jobHandler, err := billing.NewStripeWebhookJobHandler(recorder)
	require.NoError(t, err)
	worker, err := jobs.NewWorker(jobs.WorkerConfig{
		Store: store, Queue: billing.StripeWebhookQueue, Handler: jobHandler,
		WorkerID: "stripe-integration-test", BatchSize: 1,
	})
	require.NoError(t, err)

	secret := "whsec_generic_job_integration"
	eventID := "evt_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	body := []byte(fmt.Sprintf(
		`{"id":%q,"type":"invoice.paid","created":1700000000,"api_version":"2026-06-30.basil","livemode":false,"data":{"object":{}}}`,
		eventID,
	))
	timestamp := time.Now().Unix()
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(strconv.FormatInt(timestamp, 10)))
	_, _ = mac.Write([]byte("."))
	_, _ = mac.Write(body)

	request := httptest.NewRequest(http.MethodPost, "/v1/billing/webhook", bytes.NewReader(body))
	request.Header.Set("Stripe-Signature", fmt.Sprintf(
		"t=%d,v1=%s", timestamp, hex.EncodeToString(mac.Sum(nil)),
	))
	response := httptest.NewRecorder()
	billing.NewHandler(billing.HandlerDeps{
		Producer: store, WebhookSecret: secret,
	}).ServeHTTP(response, request)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	require.JSONEq(t, `{"status":"queued"}`, response.Body.String())

	processed, err := worker.RunOnce(testCtx)
	require.NoError(t, err)
	require.Equal(t, 1, processed)
	require.NotNil(t, recorder.event)
	require.Equal(t, eventID, recorder.event.GetEventId())
	require.Equal(t, body, recorder.event.GetRawBody())

	var state string
	var attempts int
	require.NoError(t, pool.QueryRow(testCtx, `
		SELECT state, attempt_count
		FROM job_messages
		WHERE direction = 'inbox'
		  AND queue = $1
		  AND topic = $2
		  AND idempotency_key = $3`,
		billing.StripeWebhookQueue, billing.StripeWebhookTopic, eventID,
	).Scan(&state, &attempts))
	require.Equal(t, "succeeded", state)
	require.Equal(t, 1, attempts)

	var legacyTable *string
	require.NoError(t, pool.QueryRow(testCtx,
		`SELECT to_regclass('public.stripe_webhook_events')::text`,
	).Scan(&legacyTable))
	require.Nil(t, legacyTable, "Stripe must not retain a specialized queue table")
}
