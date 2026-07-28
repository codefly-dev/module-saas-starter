package analytics

import (
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"

	analyticsv1 "accounts/pkg/gen/saas/analytics/v1"
)

//go:embed registry.json
var defaultRegistryJSON []byte

var canonicalNamePattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,127}$`)

type Definition struct {
	Name              string
	Owner             string
	Description       string
	SchemaVersion     uint32
	PIIClassification analyticsv1.PIIClassification
	RetentionDays     int
	Purpose           analyticsv1.AnalyticsPurpose
	Sources           []analyticsv1.EventSource
	Properties        []string
	PropertyTypes     map[string]PropertyType
}

type PropertyType string

const (
	PropertyTypeString  PropertyType = "string"
	PropertyTypeNumber  PropertyType = "number"
	PropertyTypeBoolean PropertyType = "boolean"
)

type Registry struct {
	contractVersion uint32
	events          map[string]Definition
}

type registryDocument struct {
	ContractVersion uint32 `json:"contract_version"`
	Defaults        struct {
		SchemaVersion     uint32 `json:"schema_version"`
		PIIClassification string `json:"pii_classification"`
		RetentionDays     int    `json:"retention_days"`
		PropertyType      string `json:"property_type"`
	} `json:"defaults"`
	PropertyTypes map[string]string `json:"property_types"`
	Events        []struct {
		Name        string   `json:"name"`
		Owner       string   `json:"owner"`
		Description string   `json:"description"`
		Sources     []string `json:"sources"`
		Purpose     string   `json:"purpose"`
		Properties  []string `json:"properties"`
	} `json:"events"`
}

var (
	defaultRegistryOnce sync.Once
	defaultRegistry     *Registry
	defaultRegistryErr  error
)

func DefaultRegistry() (*Registry, error) {
	defaultRegistryOnce.Do(func() {
		defaultRegistry, defaultRegistryErr = ParseRegistry(defaultRegistryJSON)
	})
	return defaultRegistry, defaultRegistryErr
}

func ParseRegistry(body []byte) (*Registry, error) {
	decoder := json.NewDecoder(strings.NewReader(string(body)))
	decoder.DisallowUnknownFields()
	var document registryDocument
	if err := decoder.Decode(&document); err != nil {
		return nil, fmt.Errorf("analytics: parse event registry: %w", err)
	}
	if document.ContractVersion == 0 {
		return nil, errors.New("analytics: registry contract version must be positive")
	}
	if document.Defaults.SchemaVersion == 0 {
		return nil, errors.New("analytics: default schema version must be positive")
	}
	if document.Defaults.RetentionDays <= 0 {
		return nil, errors.New("analytics: default retention must be positive")
	}
	classification, err := parsePIIClassification(document.Defaults.PIIClassification)
	if err != nil {
		return nil, err
	}
	defaultPropertyType, err := parsePropertyType(document.Defaults.PropertyType)
	if err != nil {
		return nil, fmt.Errorf("analytics: default property type: %w", err)
	}
	propertyTypes := make(map[string]PropertyType, len(document.PropertyTypes))
	for name, value := range document.PropertyTypes {
		if !canonicalNamePattern.MatchString(name) || forbiddenPropertyName(name) {
			return nil, fmt.Errorf("analytics: property type key %q is unsafe", name)
		}
		propertyType, err := parsePropertyType(value)
		if err != nil {
			return nil, fmt.Errorf("analytics: property %q: %w", name, err)
		}
		propertyTypes[name] = propertyType
	}
	usedPropertyTypes := make(map[string]struct{}, len(propertyTypes))

	registry := &Registry{
		contractVersion: document.ContractVersion,
		events:          make(map[string]Definition, len(document.Events)),
	}
	for _, item := range document.Events {
		if !canonicalNamePattern.MatchString(item.Name) {
			return nil, fmt.Errorf("analytics: event name %q is not canonical", item.Name)
		}
		if _, exists := registry.events[item.Name]; exists {
			return nil, fmt.Errorf("analytics: duplicate event %q", item.Name)
		}
		if strings.TrimSpace(item.Owner) == "" || strings.TrimSpace(item.Description) == "" {
			return nil, fmt.Errorf("analytics: event %q requires owner and description", item.Name)
		}
		purpose, err := parsePurpose(item.Purpose)
		if err != nil {
			return nil, fmt.Errorf("analytics: event %q: %w", item.Name, err)
		}
		if len(item.Sources) == 0 {
			return nil, fmt.Errorf("analytics: event %q requires at least one source", item.Name)
		}
		sources := make([]analyticsv1.EventSource, 0, len(item.Sources))
		seenSources := map[analyticsv1.EventSource]struct{}{}
		for _, sourceName := range item.Sources {
			source, err := parseSource(sourceName)
			if err != nil {
				return nil, fmt.Errorf("analytics: event %q: %w", item.Name, err)
			}
			if _, exists := seenSources[source]; exists {
				return nil, fmt.Errorf("analytics: event %q repeats source %q", item.Name, sourceName)
			}
			seenSources[source] = struct{}{}
			sources = append(sources, source)
		}
		properties := append([]string(nil), item.Properties...)
		eventPropertyTypes := make(map[string]PropertyType, len(properties))
		sort.Strings(properties)
		for index, property := range properties {
			if !canonicalNamePattern.MatchString(property) {
				return nil, fmt.Errorf("analytics: event %q property %q is not canonical", item.Name, property)
			}
			if forbiddenPropertyName(property) {
				return nil, fmt.Errorf("analytics: event %q property %q is forbidden", item.Name, property)
			}
			if index > 0 && property == properties[index-1] {
				return nil, fmt.Errorf("analytics: event %q repeats property %q", item.Name, property)
			}
			propertyType := defaultPropertyType
			if override, ok := propertyTypes[property]; ok {
				propertyType = override
				usedPropertyTypes[property] = struct{}{}
			}
			eventPropertyTypes[property] = propertyType
		}
		registry.events[item.Name] = Definition{
			Name:              item.Name,
			Owner:             item.Owner,
			Description:       item.Description,
			SchemaVersion:     document.Defaults.SchemaVersion,
			PIIClassification: classification,
			RetentionDays:     document.Defaults.RetentionDays,
			Purpose:           purpose,
			Sources:           sources,
			Properties:        properties,
			PropertyTypes:     eventPropertyTypes,
		}
	}
	if len(registry.events) == 0 {
		return nil, errors.New("analytics: event registry is empty")
	}
	for property := range propertyTypes {
		if _, used := usedPropertyTypes[property]; !used {
			return nil, fmt.Errorf("analytics: property type override %q is unused", property)
		}
	}
	return registry, nil
}

func (r *Registry) ContractVersion() uint32 {
	if r == nil {
		return 0
	}
	return r.contractVersion
}

func (r *Registry) Definition(name string) (Definition, bool) {
	if r == nil {
		return Definition{}, false
	}
	definition, ok := r.events[name]
	if !ok {
		return Definition{}, false
	}
	definition.Sources = append([]analyticsv1.EventSource(nil), definition.Sources...)
	definition.Properties = append([]string(nil), definition.Properties...)
	definition.PropertyTypes = make(map[string]PropertyType, len(definition.PropertyTypes))
	for name, propertyType := range r.events[name].PropertyTypes {
		definition.PropertyTypes[name] = propertyType
	}
	return definition, true
}

func (r *Registry) Definitions() []Definition {
	if r == nil {
		return nil
	}
	out := make([]Definition, 0, len(r.events))
	for name := range r.events {
		definition, _ := r.Definition(name)
		out = append(out, definition)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func parseSource(value string) (analyticsv1.EventSource, error) {
	switch value {
	case "web":
		return analyticsv1.EventSource_EVENT_SOURCE_WEB, nil
	case "api":
		return analyticsv1.EventSource_EVENT_SOURCE_API, nil
	case "worker":
		return analyticsv1.EventSource_EVENT_SOURCE_WORKER, nil
	case "webhook":
		return analyticsv1.EventSource_EVENT_SOURCE_WEBHOOK, nil
	case "import":
		return analyticsv1.EventSource_EVENT_SOURCE_IMPORT, nil
	default:
		return analyticsv1.EventSource_EVENT_SOURCE_UNSPECIFIED,
			fmt.Errorf("unknown event source %q", value)
	}
}

func parsePurpose(value string) (analyticsv1.AnalyticsPurpose, error) {
	switch value {
	case "essential":
		return analyticsv1.AnalyticsPurpose_ANALYTICS_PURPOSE_ESSENTIAL, nil
	case "product":
		return analyticsv1.AnalyticsPurpose_ANALYTICS_PURPOSE_PRODUCT, nil
	case "marketing":
		return analyticsv1.AnalyticsPurpose_ANALYTICS_PURPOSE_MARKETING, nil
	case "replay":
		return analyticsv1.AnalyticsPurpose_ANALYTICS_PURPOSE_REPLAY, nil
	default:
		return analyticsv1.AnalyticsPurpose_ANALYTICS_PURPOSE_UNSPECIFIED,
			fmt.Errorf("unknown analytics purpose %q", value)
	}
}

func parsePIIClassification(value string) (analyticsv1.PIIClassification, error) {
	switch value {
	case "none":
		return analyticsv1.PIIClassification_PII_CLASSIFICATION_NONE, nil
	case "pseudonymous":
		return analyticsv1.PIIClassification_PII_CLASSIFICATION_PSEUDONYMOUS, nil
	case "internal":
		return analyticsv1.PIIClassification_PII_CLASSIFICATION_INTERNAL, nil
	default:
		return analyticsv1.PIIClassification_PII_CLASSIFICATION_UNSPECIFIED,
			fmt.Errorf("analytics: unknown PII classification %q", value)
	}
}

func parsePropertyType(value string) (PropertyType, error) {
	switch PropertyType(value) {
	case PropertyTypeString, PropertyTypeNumber, PropertyTypeBoolean:
		return PropertyType(value), nil
	default:
		return "", fmt.Errorf("unknown property type %q", value)
	}
}
