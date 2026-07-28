package analytics

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	analyticsv1 "accounts/pkg/gen/saas/analytics/v1"
)

const postHogBatchLimit = 100

type PostHogConfig struct {
	APIKey     string
	Host       string
	Timeout    time.Duration
	HTTPClient *http.Client
}

type PostHog struct {
	apiKey   string
	endpoint *url.URL
	client   *http.Client
}

func NewPostHog(config PostHogConfig) (*PostHog, error) {
	if strings.TrimSpace(config.APIKey) == "" {
		return nil, errors.New("analytics: PostHog API key is required")
	}
	if strings.TrimSpace(config.Host) == "" {
		return nil, errors.New("analytics: PostHog host is required")
	}
	host, err := url.Parse(config.Host)
	if err != nil || host.Scheme == "" || host.Host == "" {
		return nil, errors.New("analytics: PostHog host must be an absolute URL")
	}
	localHost := host.Hostname() == "localhost" || host.Hostname() == "127.0.0.1"
	if host.Scheme != "https" && (host.Scheme != "http" || !localHost) {
		return nil, errors.New("analytics: PostHog host must use HTTPS")
	}
	timeout := config.Timeout
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	client := config.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: timeout}
	} else if client.Timeout <= 0 {
		cloned := *client
		cloned.Timeout = timeout
		client = &cloned
	}
	endpoint := host.ResolveReference(&url.URL{Path: "/batch/"})
	return &PostHog{apiKey: config.APIKey, endpoint: endpoint, client: client}, nil
}

func (p *PostHog) Capture(
	ctx context.Context,
	event *analyticsv1.ProductEvent,
) (Delivery, error) {
	if err := p.CaptureBatch(ctx, []*analyticsv1.ProductEvent{event}); err != nil {
		return Delivery{}, err
	}
	return Delivery{Reference: event.GetEventId()}, nil
}

func (p *PostHog) CaptureBatch(
	ctx context.Context,
	events []*analyticsv1.ProductEvent,
) error {
	if len(events) == 0 || len(events) > postHogBatchLimit {
		return fmt.Errorf("analytics: PostHog batch must contain 1 to %d events", postHogBatchLimit)
	}
	batch := make([]map[string]any, 0, len(events))
	for _, event := range events {
		if event == nil {
			return errors.New("analytics: PostHog event is required")
		}
		batch = append(batch, postHogEvent(event))
	}
	return p.send(ctx, map[string]any{"api_key": p.apiKey, "batch": batch})
}

func (p *PostHog) Identify(ctx context.Context, command Identity) error {
	if command.DistinctID == "" {
		return errors.New("analytics: identify distinct id is required")
	}
	properties := map[string]any{"distinct_id": command.DistinctID}
	if command.OrganizationID != "" {
		properties["$groups"] = map[string]string{"organization": command.OrganizationID}
	}
	if len(command.Properties) > 0 {
		properties["$set"] = command.Properties
	}
	return p.sendEvents(ctx, map[string]any{"event": "$identify", "properties": properties})
}

func (p *PostHog) Alias(ctx context.Context, command Alias) error {
	if command.PreviousID == "" || command.DistinctID == "" ||
		command.PreviousID == command.DistinctID {
		return errors.New("analytics: alias requires two different identities")
	}
	return p.sendEvents(ctx, map[string]any{
		"event": "$create_alias",
		"properties": map[string]any{
			"distinct_id": command.PreviousID,
			"alias":       command.DistinctID,
		},
	})
}

func (p *PostHog) Group(ctx context.Context, command Group) error {
	if command.OrganizationID == "" {
		return errors.New("analytics: group organization id is required")
	}
	return p.sendEvents(ctx, map[string]any{
		"event": "$groupidentify",
		"properties": map[string]any{
			"distinct_id": command.OrganizationID,
			"$group_type": "organization",
			"$group_key":  command.OrganizationID,
			"$group_set":  command.Properties,
		},
	})
}

func (p *PostHog) Suppress(context.Context, Suppression) error {
	return errors.New("analytics: PostHog suppression requires a configured personal-API deletion adapter")
}

func (p *PostHog) sendEvents(ctx context.Context, event map[string]any) error {
	return p.send(ctx, map[string]any{"api_key": p.apiKey, "batch": []map[string]any{event}})
}

func (p *PostHog) send(ctx context.Context, payload map[string]any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("analytics: encode PostHog request: %w", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, p.endpoint.String(), bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("analytics: create PostHog request: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := p.client.Do(request)
	if err != nil {
		return fmt.Errorf("analytics: deliver to PostHog: %w", err)
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 64*1024))
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("analytics: PostHog returned HTTP %d", response.StatusCode)
	}
	return nil
}

func postHogEvent(event *analyticsv1.ProductEvent) map[string]any {
	distinctID := event.GetActorUserId()
	if distinctID == "" {
		distinctID = event.GetAnonymousId()
	}
	if distinctID == "" {
		distinctID = event.GetOrganizationId()
	}
	properties := make(map[string]any, len(event.GetProperties().GetFields())+12)
	if event.GetProperties() != nil {
		for key, value := range event.GetProperties().AsMap() {
			properties[key] = value
		}
	}
	properties["distinct_id"] = distinctID
	properties["event_id"] = event.GetEventId()
	properties["schema_version"] = event.GetSchemaVersion()
	properties["source"] = event.GetSource().String()
	if event.GetOrganizationId() != "" {
		properties["$groups"] = map[string]string{"organization": event.GetOrganizationId()}
	}
	if event.GetSessionId() != "" {
		properties["$session_id"] = event.GetSessionId()
	}
	context := event.GetContext()
	if context != nil {
		properties["route"] = context.GetRoute()
		properties["release"] = context.GetRelease()
		properties["environment"] = context.GetEnvironment()
		properties["locale"] = context.GetLocale()
		properties["device"] = context.GetDevice()
		properties["first_touch_source"] = context.GetFirstTouchSource()
		properties["first_touch_campaign"] = context.GetFirstTouchCampaign()
		properties["last_touch_source"] = context.GetLastTouchSource()
		properties["last_touch_campaign"] = context.GetLastTouchCampaign()
		for key, value := range context.GetFeatureFlags() {
			properties["feature_flag."+key] = value
		}
	}
	return map[string]any{
		"event":      event.GetEventName(),
		"uuid":       event.GetEventId(),
		"timestamp":  event.GetOccurredAt().AsTime().Format(time.RFC3339Nano),
		"properties": properties,
	}
}
