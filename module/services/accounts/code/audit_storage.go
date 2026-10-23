package main

import (
	"errors"
	"fmt"

	"accounts/pkg/auditstorage"
	"accounts/pkg/business"
)

// errWarehouseAuditStorageUnavailable is why `warehouse` is refused at startup
// until the warehouse AuditStorage exists.
var errWarehouseAuditStorageUnavailable = errors.New(
	"AUDIT_STORAGE_BACKEND=warehouse is not available yet: this build has no warehouse audit storage, " +
		"and a host that accepted the setting would journal every audit entry and never append one; " +
		"set it to postgres, or leave the `audit-storage` group unset")

// buildWarehouseAuditStorage builds the warehouse AuditStorage for the group's
// dataset and table. It is the seam the warehouse implementation plugs into: it
// is given the group's names only, and reaches the warehouse through the service
// the composition provides, never through an endpoint or a token configured
// here. Until that implementation lands it refuses.
func buildWarehouseAuditStorage(auditstorage.Config) (business.AuditStorage, error) {
	return nil, errWarehouseAuditStorageUnavailable
}

// selectAuditStorage makes the one choice of audit backend, at startup, from the
// `audit-storage` group and AUDIT_SINK: the backend, and for the warehouse the
// storage built by build. Combinations that cannot work are refused here by
// name, before the host serves anything.
//
// AUDIT_SINK=both is refused with the warehouse. The tee copies audit_events
// rows to an HTTP endpoint, and the warehouse backend writes none: there would
// be nothing to copy, and the setting would read as an export that never runs.
// The warehouse is a swap of the store of record, not a second copy beside it.
func selectAuditStorage(
	config auditstorage.Config,
	sink auditSinkMode,
	build func(auditstorage.Config) (business.AuditStorage, error),
) (business.AuditBackend, business.AuditStorage, error) {
	switch config.Backend {
	case business.AuditBackendPostgres:
		return business.AuditBackendPostgres, nil, nil
	case business.AuditBackendWarehouse:
		if sink == auditSinkBoth {
			return "", nil, errors.New(
				"AUDIT_SINK=both cannot be combined with AUDIT_STORAGE_BACKEND=warehouse: the tee copies audit_events rows and the warehouse backend writes none; " +
					"the warehouse replaces audit_events as the store of record, so unset AUDIT_SINK or set it to postgres")
		}
		storage, err := build(config)
		if err != nil {
			return "", nil, err
		}
		if storage == nil {
			return "", nil, fmt.Errorf("the warehouse audit storage for %s.%s was not built", config.Dataset, config.Table)
		}
		return business.AuditBackendWarehouse, storage, nil
	default:
		return "", nil, fmt.Errorf("unknown audit storage backend %q", config.Backend)
	}
}
