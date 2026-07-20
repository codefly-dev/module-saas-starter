package billing

// Stripe webhook receipt and asynchronous event processing.
//
// The public HTTP path has one responsibility: verify the exact request body
// and durably insert a versioned payload into the generic jobs inbox. It never
// calls Stripe, updates billing state, or sends email. Once that transaction
// commits it can safely return a 2xx; the generic leased worker processes the
// retained payload independently.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	billingv1 "accounts/pkg/gen/saas/billing/v1"
	jobsv1 "accounts/pkg/gen/saas/jobs/v1"
	"accounts/pkg/jobs"

	"github.com/codefly-dev/core/wool"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const (
	StripeWebhookQueue         = "billing"
	StripeWebhookTopic         = "stripe.webhook.process"
	StripeWebhookSource        = "stripe.webhook"
	StripeWebhookSchemaVersion = 1
	StripeWebhookMaxAttempts   = 8
	stripeWebhookContentType   = "application/protobuf"
	maxStripeWebhookBody       = 960 * 1024
)

// BillingEmail is the immutable command passed to the durable email outbox.
type BillingEmail struct {
	DeliveryKey    string
	OrganizationID string
	Template       string
	To             string
	Variables      map[string]string
}

// EmailNotifier is the optional durable email integration used for
// billing-related notifications. An enqueue failure is returned to the Stripe
// job worker so its stable event id can be retried without duplicate email.
type EmailNotifier interface {
	EnqueueBillingEmail(ctx context.Context, message BillingEmail) error
}

// HandlerDeps are deliberately limited to receipt-time dependencies.
type HandlerDeps struct {
	Producer      jobs.Producer
	WebhookSecret string
}

// NewHandler returns the fast, public Stripe webhook endpoint.
func NewHandler(deps HandlerDeps) http.Handler {
	return &handler{deps: deps}
}

type handler struct {
	deps HandlerDeps
}

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "POST required")
		return
	}

	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxStripeWebhookBody))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}

	ev, err := ParseEvent(body, r.Header.Get("Stripe-Signature"), h.deps.WebhookSecret)
	if err != nil {
		wool.Get(r.Context()).In("billing.webhook").Warn("webhook verification failed", wool.ErrField(err))
		writeError(w, http.StatusBadRequest, "invalid signature")
		return
	}
	if h.deps.Producer == nil {
		wool.Get(r.Context()).In("billing.webhook").Warn("webhook inbox is not configured")
		writeError(w, http.StatusServiceUnavailable, "unavailable")
		return
	}

	payload := &billingv1.StripeWebhookJob{
		EventId: ev.ID, EventType: ev.Type, RawBody: body,
		ApiVersion: ev.APIVersion, Livemode: ev.Livemode,
	}
	if ev.Created > 0 {
		payload.StripeCreatedAt = timestamppb.New(time.Unix(ev.Created, 0).UTC())
	}
	if err := jobs.ValidateCommand(payload); err != nil {
		wool.Get(r.Context()).In("billing.webhook").Warn("verified webhook contract is invalid")
		writeError(w, http.StatusBadRequest, "invalid event")
		return
	}
	encoded, err := (proto.MarshalOptions{Deterministic: true}).Marshal(payload)
	if err != nil {
		wool.Get(r.Context()).In("billing.webhook").Warn("encode webhook job failed", wool.ErrField(err))
		writeError(w, http.StatusInternalServerError, "internal")
		return
	}
	response, err := h.deps.Producer.EnqueueJob(r.Context(), &jobsv1.EnqueueJobRequest{
		Job: &jobsv1.NewJob{
			Direction:      jobsv1.JobDirection_JOB_DIRECTION_INBOX,
			Scope:          &jobsv1.JobScope{Value: &jobsv1.JobScope_Global{Global: true}},
			Queue:          StripeWebhookQueue,
			Topic:          StripeWebhookTopic,
			Source:         StripeWebhookSource,
			IdempotencyKey: ev.ID,
			SchemaVersion:  StripeWebhookSchemaVersion,
			Payload:        encoded,
			ContentType:    stripeWebhookContentType,
			MaxAttempts:    StripeWebhookMaxAttempts,
		},
	})
	if err != nil {
		// No durable receipt means Stripe must retry delivery.
		wool.Get(r.Context()).In("billing.webhook").Warn("persist webhook failed", wool.ErrField(err))
		if errors.Is(err, jobs.ErrIdempotencyConflict) {
			writeError(w, http.StatusConflict, "event conflict")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal")
		return
	}
	switch response.GetDisposition() {
	case jobsv1.JobEnqueueDisposition_JOB_ENQUEUE_DISPOSITION_DUPLICATE:
		writeJSON(w, http.StatusOK, map[string]string{"status": "duplicate"})
	case jobsv1.JobEnqueueDisposition_JOB_ENQUEUE_DISPOSITION_INSERTED:
		writeJSON(w, http.StatusOK, map[string]string{"status": "queued"})
	default:
		wool.Get(r.Context()).In("billing.webhook").Warn("persist webhook returned no durable disposition")
		writeError(w, http.StatusInternalServerError, "internal")
	}
}

// EventProcessor is implemented by the billing projector and can be replaced
// with a deterministic fake in worker tests.
type EventProcessor interface {
	ProcessWebhook(ctx context.Context, event *billingv1.StripeWebhookJob) error
}

// NewStripeWebhookJobHandler adapts the versioned billing payload to the
// product-neutral worker. Contract/routing failures are permanent and safe to
// retain; projector/provider failures stay retryable and are redacted by the
// generic worker.
func NewStripeWebhookJobHandler(processor EventProcessor) (jobs.Handler, error) {
	if processor == nil {
		return nil, errors.New("billing: Stripe webhook processor is required")
	}
	return func(ctx context.Context, envelope *jobsv1.JobEnvelope) error {
		if err := validateStripeJobEnvelope(envelope); err != nil {
			return jobs.NewProcessingError("billing.invalid_job", "invalid Stripe webhook job", false)
		}
		payload := &billingv1.StripeWebhookJob{}
		if err := proto.Unmarshal(envelope.GetPayload(), payload); err != nil {
			return jobs.NewProcessingError("billing.invalid_job", "invalid Stripe webhook job", false)
		}
		if err := jobs.ValidateCommand(payload); err != nil ||
			!jobs.PayloadIdentityMatches(envelope, payload.GetEventId()) {
			return jobs.NewProcessingError("billing.invalid_job", "invalid Stripe webhook job", false)
		}
		return processor.ProcessWebhook(ctx, payload)
	}, nil
}

func validateStripeJobEnvelope(envelope *jobsv1.JobEnvelope) error {
	if envelope == nil {
		return errors.New("billing: missing Stripe job envelope")
	}
	global, ok := envelope.GetScope().GetValue().(*jobsv1.JobScope_Global)
	if envelope.GetDirection() != jobsv1.JobDirection_JOB_DIRECTION_INBOX ||
		!ok || !global.Global ||
		envelope.GetQueue() != StripeWebhookQueue ||
		envelope.GetTopic() != StripeWebhookTopic ||
		envelope.GetSource() != StripeWebhookSource ||
		envelope.GetSchemaVersion() != StripeWebhookSchemaVersion ||
		envelope.GetMaxAttempts() != StripeWebhookMaxAttempts ||
		envelope.GetContentType() != stripeWebhookContentType {
		return errors.New("billing: unexpected Stripe job routing")
	}
	return nil
}

// StripeWebhookRetryDelay preserves the workload's bounded provider retry
// policy while lifecycle and scheduling remain owned by the generic worker.
func StripeWebhookRetryDelay(attempt uint32) time.Duration {
	schedule := [...]time.Duration{
		5 * time.Second,
		30 * time.Second,
		2 * time.Minute,
		10 * time.Minute,
		30 * time.Minute,
		2 * time.Hour,
	}
	if attempt == 0 {
		return schedule[0]
	}
	index := int(attempt - 1)
	if index >= len(schedule) {
		index = len(schedule) - 1
	}
	return schedule[index]
}

type SubscriptionReader interface {
	GetSubscription(ctx context.Context, id string) (*Subscription, error)
}

type ProcessorDeps struct {
	Store    ProcessingStore
	Client   SubscriptionReader
	Notifier EmailNotifier
	Now      func() time.Time
}

// Processor projects a stored Stripe event onto local billing state.
type Processor struct {
	deps ProcessorDeps
}

func NewProcessor(deps ProcessorDeps) *Processor {
	if deps.Now == nil {
		deps.Now = time.Now
	}
	return &Processor{deps: deps}
}

// ProcessWebhook validates the stored envelope against its inbox metadata and
// dispatches it. Every error is retryable from the worker's perspective;
// persistent configuration/data errors eventually become dead letters.
func (p *Processor) ProcessWebhook(ctx context.Context, event *billingv1.StripeWebhookJob) error {
	if p.deps.Store == nil {
		return errors.New("billing: processing store is not configured")
	}
	if err := jobs.ValidateCommand(event); err != nil {
		return fmt.Errorf("billing: invalid Stripe webhook job: %w", err)
	}
	var envelope Event
	if err := json.Unmarshal(event.GetRawBody(), &envelope); err != nil {
		return err
	}
	if envelope.ID == "" || envelope.Type == "" || envelope.ID != event.GetEventId() || envelope.Type != event.GetEventType() {
		return errors.New("billing: stored webhook metadata does not match payload")
	}

	switch event.GetEventType() {
	case "customer.subscription.created",
		"customer.subscription.updated":
		return p.handleSubscriptionChanged(ctx, event.GetRawBody())
	case "customer.subscription.deleted":
		return p.handleSubscriptionChanged(ctx, event.GetRawBody())
	case "invoice.payment_failed":
		return p.handleInvoicePaymentFailed(ctx, event.GetEventId(), event.GetRawBody())
	case "invoice.payment_succeeded", "invoice.paid":
		return p.handleInvoicePaymentSucceeded(ctx, event.GetEventId(), event.GetRawBody())
	case "customer.subscription.trial_will_end":
		return p.handleTrialWillEnd(ctx, event.GetEventId(), event.GetRawBody())
	default:
		wool.Get(ctx).In("billing.processor").Info("ignoring event type", wool.Field("event_type", event.GetEventType()))
		return nil
	}
}

func (p *Processor) handleSubscriptionChanged(ctx context.Context, body []byte) error {
	var sub Subscription
	if err := ObjectFromData(body, &sub); err != nil {
		return err
	}
	return p.reconcileSubscription(ctx, sub.ID)
}

func (p *Processor) handleInvoicePaymentFailed(ctx context.Context, eventID string, body []byte) error {
	var inv struct {
		Subscription string `json:"subscription"`
		Customer     string `json:"customer"`
	}
	if err := ObjectFromData(body, &inv); err != nil {
		return err
	}
	if inv.Subscription == "" {
		return nil
	}

	if err := p.reconcileSubscription(ctx, inv.Subscription); err != nil {
		return err
	}
	return p.notifyByCustomer(ctx, eventID, inv.Customer, "payment_failed", nil)
}

func (p *Processor) handleInvoicePaymentSucceeded(ctx context.Context, eventID string, body []byte) error {
	var inv struct {
		Subscription string `json:"subscription"`
		Customer     string `json:"customer"`
	}
	if err := ObjectFromData(body, &inv); err != nil {
		return err
	}
	if inv.Subscription == "" {
		return nil
	}
	if err := p.reconcileSubscription(ctx, inv.Subscription); err != nil {
		return err
	}
	return p.notifyByCustomer(ctx, eventID, inv.Customer, "invoice_ready", nil)
}

func (p *Processor) reconcileSubscription(ctx context.Context, subscriptionID string) error {
	if subscriptionID == "" {
		return errors.New("billing: Stripe event has no subscription id")
	}
	if p.deps.Client == nil {
		return errors.New("billing: Stripe client is required for current-state reconciliation")
	}
	// Capture before the provider read. If two reads overlap, the later-started
	// observation wins even when the older HTTP response finishes last.
	observedAt := p.deps.Now().UTC()
	subscription, err := p.deps.Client.GetSubscription(ctx, subscriptionID)
	if err != nil {
		return err
	}
	if subscription == nil || subscription.ID != subscriptionID {
		return errors.New("billing: Stripe returned a mismatched subscription")
	}
	return p.upsertFromStripe(ctx, subscription, observedAt)
}

func (p *Processor) upsertFromStripe(ctx context.Context, sub *Subscription, observedAt time.Time) error {
	orgID, err := p.deps.Store.OrgByStripeCustomerID(ctx, sub.CustomerID)
	if err != nil {
		return err
	}

	var planID string
	if priceID := sub.PrimaryPriceID(); priceID != "" {
		plan, err := p.deps.Store.PlanByStripePriceID(ctx, priceID)
		if err != nil {
			return err
		}
		planID = plan.ID
	}

	var periodStart, periodEnd *time.Time
	if sub.CurrentPeriodStart > 0 {
		t := time.Unix(sub.CurrentPeriodStart, 0)
		periodStart = &t
	}
	if sub.CurrentPeriodEnd > 0 {
		t := time.Unix(sub.CurrentPeriodEnd, 0)
		periodEnd = &t
	}

	return p.deps.Store.UpsertSubscription(ctx, SubscriptionUpsert{
		OrgID:                orgID,
		PlanID:               planID,
		Status:               sub.Status,
		StripeSubscriptionID: sub.ID,
		CurrentPeriodStart:   periodStart,
		CurrentPeriodEnd:     periodEnd,
		StateObservedAt:      observedAt,
	})
}

func (p *Processor) handleTrialWillEnd(ctx context.Context, eventID string, body []byte) error {
	var sub Subscription
	if err := ObjectFromData(body, &sub); err != nil {
		return err
	}
	return p.notifyByCustomer(ctx, eventID, sub.CustomerID, "trial_ending", nil)
}

func (p *Processor) notifyByCustomer(
	ctx context.Context,
	eventID string,
	stripeCustomerID string,
	templateName string,
	extraVars map[string]string,
) error {
	if p.deps.Notifier == nil || stripeCustomerID == "" {
		return nil
	}
	orgID, err := p.deps.Store.OrgByStripeCustomerID(ctx, stripeCustomerID)
	if err != nil {
		return err
	}
	to, err := p.deps.Store.OwnerEmailByStripeCustomerID(ctx, stripeCustomerID)
	if err != nil {
		return err
	}
	if to == "" {
		return nil
	}
	vars := map[string]string{"email": to}
	for key, value := range extraVars {
		vars[key] = value
	}
	return p.deps.Notifier.EnqueueBillingEmail(ctx, BillingEmail{
		DeliveryKey:    billingEmailDeliveryKey(eventID, templateName),
		OrganizationID: orgID,
		Template:       templateName,
		To:             to,
		Variables:      vars,
	})
}

func billingEmailDeliveryKey(eventID, templateName string) string {
	digest := sha256.Sum256([]byte(eventID + "\x00" + templateName))
	return "stripe-email/" + hex.EncodeToString(digest[:])
}

func writeError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
