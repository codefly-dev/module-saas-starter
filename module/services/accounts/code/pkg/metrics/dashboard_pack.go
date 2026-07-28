package metrics

import (
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
)

//go:embed dashboard_pack.json
var dashboardPackJSON []byte

type MetricDefinition struct {
	Key        string `json:"key"`
	Title      string `json:"title"`
	Definition string `json:"definition"`
	Source     string `json:"source"`
}

type DashboardDefinition struct {
	ID             string             `json:"id"`
	Title          string             `json:"title"`
	Owner          string             `json:"owner"`
	Timezone       string             `json:"timezone"`
	RefreshSeconds int                `json:"refresh_seconds"`
	Metrics        []MetricDefinition `json:"metrics"`
}

type DashboardPack struct {
	Version    uint32                `json:"version"`
	Dashboards []DashboardDefinition `json:"dashboards"`
}

var (
	defaultDashboardPackOnce sync.Once
	defaultDashboardPack     DashboardPack
	defaultDashboardPackErr  error
)

func DefaultDashboardPack() (DashboardPack, error) {
	defaultDashboardPackOnce.Do(func() {
		defaultDashboardPack, defaultDashboardPackErr = ParseDashboardPack(dashboardPackJSON)
	})
	return defaultDashboardPack, defaultDashboardPackErr
}

func ParseDashboardPack(body []byte) (DashboardPack, error) {
	decoder := json.NewDecoder(strings.NewReader(string(body)))
	decoder.DisallowUnknownFields()
	var pack DashboardPack
	if err := decoder.Decode(&pack); err != nil {
		return DashboardPack{}, fmt.Errorf("metrics: parse dashboard pack: %w", err)
	}
	if pack.Version == 0 {
		return DashboardPack{}, errors.New("metrics: dashboard pack version must be positive")
	}
	if len(pack.Dashboards) == 0 {
		return DashboardPack{}, errors.New("metrics: dashboard pack is empty")
	}
	dashboards := map[string]struct{}{}
	metrics := map[string]struct{}{}
	for _, dashboard := range pack.Dashboards {
		if dashboard.ID == "" || dashboard.Title == "" || dashboard.Owner == "" ||
			dashboard.Timezone == "" || dashboard.RefreshSeconds <= 0 {
			return DashboardPack{}, errors.New("metrics: dashboard metadata is incomplete")
		}
		if _, exists := dashboards[dashboard.ID]; exists {
			return DashboardPack{}, fmt.Errorf("metrics: duplicate dashboard %q", dashboard.ID)
		}
		dashboards[dashboard.ID] = struct{}{}
		if len(dashboard.Metrics) == 0 {
			return DashboardPack{}, fmt.Errorf("metrics: dashboard %q has no metrics", dashboard.ID)
		}
		for _, metric := range dashboard.Metrics {
			if metric.Key == "" || metric.Title == "" || metric.Definition == "" ||
				metric.Source == "" {
				return DashboardPack{}, fmt.Errorf(
					"metrics: dashboard %q has incomplete metric metadata",
					dashboard.ID,
				)
			}
			if _, exists := metrics[metric.Key]; exists {
				return DashboardPack{}, fmt.Errorf("metrics: duplicate metric %q", metric.Key)
			}
			metrics[metric.Key] = struct{}{}
		}
	}
	return pack, nil
}
