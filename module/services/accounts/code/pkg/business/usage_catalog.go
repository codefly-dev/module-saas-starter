package business

import (
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
)

//go:embed usage_meters.json
var defaultUsageMetersJSON []byte

type UsageAggregation string

const (
	UsageAggregationSum  UsageAggregation = "sum"
	UsageAggregationMax  UsageAggregation = "max"
	UsageAggregationLast UsageAggregation = "last"
)

type UsageVisibility string

const (
	UsageVisibilityCustomer UsageVisibility = "customer"
	UsageVisibilityOperator UsageVisibility = "operator"
)

type UsageMeterDefinition struct {
	Key                string           `json:"key"`
	DisplayName        string           `json:"display_name"`
	Unit               string           `json:"unit"`
	Aggregation        UsageAggregation `json:"aggregation"`
	Owner              string           `json:"owner"`
	Source             string           `json:"source"`
	EntitlementKey     string           `json:"entitlement_key"`
	ReconciliationRule string           `json:"reconciliation_rule"`
	Visibility         UsageVisibility  `json:"visibility"`
}

type UsageMeterCatalog struct {
	version uint32
	meters  map[string]UsageMeterDefinition
}

type usageMeterDocument struct {
	Version uint32                 `json:"version"`
	Meters  []UsageMeterDefinition `json:"meters"`
}

var (
	defaultUsageMeterCatalogOnce sync.Once
	defaultUsageMeterCatalog     *UsageMeterCatalog
	defaultUsageMeterCatalogErr  error
)

func DefaultUsageMeterCatalog() (*UsageMeterCatalog, error) {
	defaultUsageMeterCatalogOnce.Do(func() {
		defaultUsageMeterCatalog, defaultUsageMeterCatalogErr = ParseUsageMeterCatalog(defaultUsageMetersJSON)
	})
	return defaultUsageMeterCatalog, defaultUsageMeterCatalogErr
}

func ParseUsageMeterCatalog(body []byte) (*UsageMeterCatalog, error) {
	decoder := json.NewDecoder(strings.NewReader(string(body)))
	decoder.DisallowUnknownFields()
	var document usageMeterDocument
	if err := decoder.Decode(&document); err != nil {
		return nil, fmt.Errorf("parse usage meter catalog: %w", err)
	}
	if document.Version == 0 || len(document.Meters) == 0 {
		return nil, errors.New("usage meter catalog requires a version and at least one meter")
	}
	catalog := &UsageMeterCatalog{
		version: document.Version,
		meters:  make(map[string]UsageMeterDefinition, len(document.Meters)),
	}
	for _, meter := range document.Meters {
		if !usageMeterPattern.MatchString(meter.Key) ||
			!usageMeterPattern.MatchString(meter.EntitlementKey) {
			return nil, fmt.Errorf("usage meter %q has a noncanonical key", meter.Key)
		}
		if _, exists := catalog.meters[meter.Key]; exists {
			return nil, fmt.Errorf("usage meter %q is duplicated", meter.Key)
		}
		if meter.DisplayName == "" || meter.Unit == "" || meter.Owner == "" ||
			meter.Source == "" || meter.ReconciliationRule == "" {
			return nil, fmt.Errorf("usage meter %q is missing required metadata", meter.Key)
		}
		if meter.Aggregation != UsageAggregationSum &&
			meter.Aggregation != UsageAggregationMax &&
			meter.Aggregation != UsageAggregationLast {
			return nil, fmt.Errorf("usage meter %q has an unknown aggregation", meter.Key)
		}
		if meter.Visibility != UsageVisibilityCustomer &&
			meter.Visibility != UsageVisibilityOperator {
			return nil, fmt.Errorf("usage meter %q has an unknown visibility", meter.Key)
		}
		catalog.meters[meter.Key] = meter
	}
	return catalog, nil
}

func (c *UsageMeterCatalog) Definitions(visibility UsageVisibility) []UsageMeterDefinition {
	if c == nil {
		return nil
	}
	out := make([]UsageMeterDefinition, 0, len(c.meters))
	for _, meter := range c.meters {
		if visibility == "" || meter.Visibility == visibility {
			out = append(out, meter)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}
