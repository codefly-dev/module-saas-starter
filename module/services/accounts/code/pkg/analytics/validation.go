package analytics

import (
	"errors"
	"fmt"
	"math"
	"regexp"
	"strings"
	"time"

	analyticsv1 "accounts/pkg/gen/saas/analytics/v1"

	"buf.build/go/protovalidate"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/structpb"
)

const (
	maxEventBytes      = 32 * 1024
	maxProperties      = 32
	maxPropertyDepth   = 2
	maxPropertyList    = 16
	maxPropertyString  = 256
	maxFutureClockSkew = 5 * time.Minute
)

var (
	forbiddenPropertyPattern = regexp.MustCompile(
		`(^|_)(api_key|authorization|cookie|email|message|body|content|password|phone|secret|token)($|_)`,
	)
	credentialValuePattern = regexp.MustCompile(
		`(?i)(bearer\s+[a-z0-9._~+/=-]{12,}|(?:sk|pk|rk)_(?:live|test)_[a-z0-9]{12,})`,
	)
	emailValuePattern = regexp.MustCompile(
		`(?i)(^|[^a-z0-9.!#$%&'*+/=?^_` + "`" + `{|}~-])[a-z0-9.!#$%&'*+/=?^_` + "`" + `{|}~-]+@[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?(?:\.[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?)+($|[^a-z0-9.-])`,
	)
)

func (r *Registry) Validate(event *analyticsv1.ProductEvent) error {
	if event == nil {
		return errors.New("analytics: product event is required")
	}
	validator, err := protovalidate.New()
	if err != nil {
		return fmt.Errorf("analytics: initialize event validator: %w", err)
	}
	if err := validator.Validate(event); err != nil {
		return fmt.Errorf("analytics: invalid event envelope: %w", err)
	}
	definition, ok := r.Definition(event.GetEventName())
	if !ok {
		return fmt.Errorf("analytics: event %q is not registered", event.GetEventName())
	}
	if event.GetSchemaVersion() != definition.SchemaVersion {
		return fmt.Errorf(
			"analytics: event %q schema version %d does not match registry version %d",
			event.GetEventName(), event.GetSchemaVersion(), definition.SchemaVersion,
		)
	}
	if !sourceAllowed(definition.Sources, event.GetSource()) {
		return fmt.Errorf("analytics: event %q does not allow source %s", event.GetEventName(), event.GetSource())
	}
	if event.GetPrivacy().GetPurpose() != definition.Purpose {
		return fmt.Errorf("analytics: event %q purpose does not match registry", event.GetEventName())
	}
	if event.GetPrivacy().GetPiiClassification() != definition.PIIClassification {
		return fmt.Errorf("analytics: event %q PII classification does not match registry", event.GetEventName())
	}
	consent := event.GetPrivacy().GetConsentState()
	if consent == analyticsv1.ConsentState_CONSENT_STATE_DENIED ||
		consent == analyticsv1.ConsentState_CONSENT_STATE_WITHDRAWN {
		return errors.New("analytics: resolved consent forbids collection")
	}
	if event.GetSource() == analyticsv1.EventSource_EVENT_SOURCE_WEB &&
		definition.Purpose != analyticsv1.AnalyticsPurpose_ANALYTICS_PURPOSE_ESSENTIAL &&
		consent != analyticsv1.ConsentState_CONSENT_STATE_GRANTED {
		return errors.New("analytics: optional browser collection requires granted consent")
	}
	if event.GetSource() == analyticsv1.EventSource_EVENT_SOURCE_WEB &&
		event.GetActorUserId() == "" && event.GetAnonymousId() == "" {
		return errors.New("analytics: browser event requires an actor or anonymous identifier")
	}
	occurredAt := event.GetOccurredAt().AsTime()
	receivedAt := event.GetReceivedAt().AsTime()
	if occurredAt.After(receivedAt.Add(maxFutureClockSkew)) {
		return errors.New("analytics: event occurred_at exceeds allowed clock skew")
	}
	if proto.Size(event) > maxEventBytes {
		return fmt.Errorf("analytics: event exceeds %d bytes", maxEventBytes)
	}
	if err := validateContext(event.GetContext()); err != nil {
		return fmt.Errorf("analytics: event %q context: %w", event.GetEventName(), err)
	}
	if err := validateProperties(definition, event.GetProperties()); err != nil {
		return fmt.Errorf("analytics: event %q: %w", event.GetEventName(), err)
	}
	return nil
}

func validateContext(context *analyticsv1.EventContext) error {
	if context == nil {
		return nil
	}
	if route := context.GetRoute(); strings.ContainsAny(route, "?#") {
		return errors.New("route must not contain a query or fragment")
	}
	values := map[string]string{
		"route":                context.GetRoute(),
		"release":              context.GetRelease(),
		"environment":          context.GetEnvironment(),
		"locale":               context.GetLocale(),
		"device":               context.GetDevice(),
		"first_touch_source":   context.GetFirstTouchSource(),
		"first_touch_campaign": context.GetFirstTouchCampaign(),
		"last_touch_source":    context.GetLastTouchSource(),
		"last_touch_campaign":  context.GetLastTouchCampaign(),
	}
	for name, value := range values {
		if err := validateSensitiveString(value); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
	}
	for name, value := range context.GetFeatureFlags() {
		if err := validateSensitiveString(value); err != nil {
			return fmt.Errorf("feature flag %q: %w", name, err)
		}
	}
	return nil
}

func sourceAllowed(allowed []analyticsv1.EventSource, source analyticsv1.EventSource) bool {
	for _, candidate := range allowed {
		if candidate == source {
			return true
		}
	}
	return false
}

func validateProperties(definition Definition, properties *structpb.Struct) error {
	if properties == nil {
		return nil
	}
	if len(properties.GetFields()) > maxProperties {
		return fmt.Errorf("properties contain more than %d entries", maxProperties)
	}
	allowed := make(map[string]PropertyType, len(definition.Properties))
	for _, property := range definition.Properties {
		allowed[property] = definition.PropertyTypes[property]
	}
	for key, value := range properties.GetFields() {
		if forbiddenPropertyName(key) {
			return fmt.Errorf("property %q is forbidden", key)
		}
		propertyType, ok := allowed[key]
		if !ok {
			return fmt.Errorf("property %q is not registered", key)
		}
		if err := validatePropertyValue(value, 0); err != nil {
			return fmt.Errorf("property %q: %w", key, err)
		}
		if err := validatePropertyType(value, propertyType); err != nil {
			return fmt.Errorf("property %q: %w", key, err)
		}
	}
	return nil
}

func validatePropertyType(value *structpb.Value, propertyType PropertyType) error {
	switch propertyType {
	case PropertyTypeString:
		if _, ok := value.GetKind().(*structpb.Value_StringValue); !ok {
			return errors.New("must be a string")
		}
	case PropertyTypeNumber:
		if _, ok := value.GetKind().(*structpb.Value_NumberValue); !ok {
			return errors.New("must be a number")
		}
	case PropertyTypeBoolean:
		if _, ok := value.GetKind().(*structpb.Value_BoolValue); !ok {
			return errors.New("must be a boolean")
		}
	default:
		return errors.New("has an unsupported registry type")
	}
	return nil
}

func forbiddenPropertyName(name string) bool {
	return forbiddenPropertyPattern.MatchString(name)
}

func validatePropertyValue(value *structpb.Value, depth int) error {
	if value == nil {
		return errors.New("value is missing")
	}
	if depth > maxPropertyDepth {
		return fmt.Errorf("nesting exceeds %d levels", maxPropertyDepth)
	}
	switch kind := value.GetKind().(type) {
	case *structpb.Value_NullValue, *structpb.Value_BoolValue:
		return nil
	case *structpb.Value_NumberValue:
		if math.IsNaN(kind.NumberValue) || math.IsInf(kind.NumberValue, 0) {
			return errors.New("number must be finite")
		}
		return nil
	case *structpb.Value_StringValue:
		if len(kind.StringValue) > maxPropertyString {
			return fmt.Errorf("string exceeds %d bytes", maxPropertyString)
		}
		return validateSensitiveString(kind.StringValue)
	case *structpb.Value_ListValue:
		if len(kind.ListValue.GetValues()) > maxPropertyList {
			return fmt.Errorf("list contains more than %d values", maxPropertyList)
		}
		for _, item := range kind.ListValue.GetValues() {
			if err := validatePropertyValue(item, depth+1); err != nil {
				return err
			}
		}
		return nil
	case *structpb.Value_StructValue:
		if len(kind.StructValue.GetFields()) > maxProperties {
			return fmt.Errorf("object contains more than %d entries", maxProperties)
		}
		for key, item := range kind.StructValue.GetFields() {
			if forbiddenPropertyName(key) {
				return fmt.Errorf("nested property %q is forbidden", key)
			}
			if err := validatePropertyValue(item, depth+1); err != nil {
				return err
			}
		}
		return nil
	default:
		return errors.New("unsupported property value")
	}
}

func validateSensitiveString(value string) error {
	if credentialValuePattern.MatchString(value) {
		return errors.New("value resembles a credential")
	}
	if emailValuePattern.MatchString(value) {
		return errors.New("email addresses are not allowed")
	}
	return nil
}
