package metrics

import (
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

//go:embed slo_pack.json
var sloPackJSON []byte

type SLODefinition struct {
	ID                 string  `json:"id"`
	Owner              string  `json:"owner"`
	AvailabilityTarget float64 `json:"availability_target"`
	LatencyP95MS       int64   `json:"latency_p95_ms"`
	Success            string  `json:"success"`
	Population         string  `json:"population"`
}

type AlertDefinition struct {
	ID        string `json:"id"`
	Signal    string `json:"signal"`
	Condition string `json:"condition"`
	Runbook   string `json:"runbook"`
}

type SLOPack struct {
	Version    uint32            `json:"version"`
	WindowDays int               `json:"window_days"`
	SLOs       []SLODefinition   `json:"slos"`
	Alerts     []AlertDefinition `json:"alerts"`
}

func DefaultSLOPack() (SLOPack, error) {
	return ParseSLOPack(sloPackJSON)
}

func ParseSLOPack(body []byte) (SLOPack, error) {
	decoder := json.NewDecoder(strings.NewReader(string(body)))
	decoder.DisallowUnknownFields()
	var pack SLOPack
	if err := decoder.Decode(&pack); err != nil {
		return SLOPack{}, fmt.Errorf("metrics: parse SLO pack: %w", err)
	}
	if pack.Version == 0 || pack.WindowDays <= 0 {
		return SLOPack{}, errors.New("metrics: SLO pack version and window are required")
	}
	ids := map[string]struct{}{}
	for _, slo := range pack.SLOs {
		if slo.ID == "" || slo.Owner == "" || slo.AvailabilityTarget <= 0 ||
			slo.AvailabilityTarget > 1 || slo.LatencyP95MS <= 0 ||
			slo.Success == "" || slo.Population == "" {
			return SLOPack{}, errors.New("metrics: SLO definition is incomplete")
		}
		if _, exists := ids[slo.ID]; exists {
			return SLOPack{}, fmt.Errorf("metrics: duplicate SLO %q", slo.ID)
		}
		ids[slo.ID] = struct{}{}
	}
	if len(pack.SLOs) == 0 || len(pack.Alerts) == 0 {
		return SLOPack{}, errors.New("metrics: SLOs and alerts are required")
	}
	for _, alert := range pack.Alerts {
		if alert.ID == "" || alert.Signal == "" || alert.Condition == "" || alert.Runbook == "" {
			return SLOPack{}, errors.New("metrics: alert definition is incomplete")
		}
		if _, exists := ids[alert.ID]; exists {
			return SLOPack{}, fmt.Errorf("metrics: duplicate SLO or alert %q", alert.ID)
		}
		ids[alert.ID] = struct{}{}
	}
	return pack, nil
}
