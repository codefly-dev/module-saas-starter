package metrics

import (
	_ "embed"
	"encoding/json"
	"io"
)

//go:embed warehouse_schema.sql
var warehouseSchemaSQL string

type SeedBundle struct {
	DashboardPack      DashboardPack `json:"dashboard_pack"`
	SLOPack            SLOPack       `json:"slo_pack"`
	WarehouseSchemaSQL string        `json:"warehouse_schema_sql"`
}

func DefaultSeedBundle() (SeedBundle, error) {
	dashboards, err := DefaultDashboardPack()
	if err != nil {
		return SeedBundle{}, err
	}
	slos, err := DefaultSLOPack()
	if err != nil {
		return SeedBundle{}, err
	}
	return SeedBundle{
		DashboardPack:      dashboards,
		SLOPack:            slos,
		WarehouseSchemaSQL: warehouseSchemaSQL,
	}, nil
}

func WriteDefaultSeedBundle(writer io.Writer) error {
	bundle, err := DefaultSeedBundle()
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(writer)
	encoder.SetIndent("", "  ")
	return encoder.Encode(bundle)
}
