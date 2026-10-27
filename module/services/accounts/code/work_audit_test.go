package main

import (
	"errors"
	"strings"
	"testing"

	"accounts/pkg/auditstorage"
	"accounts/pkg/business"

	"github.com/stretchr/testify/require"
)

func TestConfiguredAuditSinkDefaultsToPostgres(t *testing.T) {
	t.Setenv("AUDIT_SINK", "")
	mode, sink, err := configuredAuditSink()
	require.NoError(t, err)
	require.Equal(t, auditSinkPostgres, mode)
	require.Nil(t, sink)

	t.Setenv("AUDIT_SINK", "postgres")
	mode, sink, err = configuredAuditSink()
	require.NoError(t, err)
	require.Equal(t, auditSinkPostgres, mode)
	require.Nil(t, sink)
}

func TestConfiguredAuditSinkBothRequiresExternalURL(t *testing.T) {
	t.Setenv("AUDIT_SINK", "both")
	t.Setenv("AUDIT_EXTERNAL_URL", "")
	_, _, err := configuredAuditSink()
	require.ErrorContains(t, err, "AUDIT_EXTERNAL_URL")

	t.Setenv("AUDIT_EXTERNAL_URL", "https://warehouse.example/audit")
	mode, sink, err := configuredAuditSink()
	require.NoError(t, err)
	require.Equal(t, auditSinkBoth, mode)
	require.IsType(t, &business.HTTPAuditSink{}, sink)
}

func TestConfiguredAuditSinkRejectsExternalOnly(t *testing.T) {
	t.Setenv("AUDIT_SINK", "external")
	_, _, err := configuredAuditSink()
	require.ErrorContains(t, err, "not permitted")
}

func TestConfiguredAuditSinkRejectsUnknownMode(t *testing.T) {
	t.Setenv("AUDIT_SINK", "kafka")
	_, _, err := configuredAuditSink()
	require.ErrorContains(t, err, "must be postgres or both")
}

// fakeWarehouseAuditStorage stands in for the warehouse AuditStorage, which is
// not built yet: selection and the write path are exercised with it.
type fakeWarehouseAuditStorage struct{ business.AuditStorage }

func TestSelectAuditStoragePostgresIsTheDefault(t *testing.T) {
	built := false
	build := func(auditstorage.Config) (business.AuditStorage, error) { built = true; return nil, nil }
	for _, sink := range []auditSinkMode{auditSinkPostgres, auditSinkBoth} {
		backend, storage, err := selectAuditStorage(auditstorage.Config{Backend: business.AuditBackendPostgres}, sink, build)
		if err != nil || backend != business.AuditBackendPostgres || storage != nil {
			t.Fatalf("sink %v: %v %v %v", sink, backend, storage, err)
		}
	}
	if built {
		t.Fatal("the warehouse storage is not built under the Postgres backend")
	}
}

func TestSelectAuditStorageWarehouseBuildsTheStorage(t *testing.T) {
	fake := fakeWarehouseAuditStorage{}
	var got auditstorage.Config
	config := auditstorage.Config{Backend: business.AuditBackendWarehouse, Dataset: "audit", Table: "entries"}
	backend, storage, err := selectAuditStorage(config, auditSinkPostgres, func(c auditstorage.Config) (business.AuditStorage, error) {
		got = c
		return fake, nil
	})
	if err != nil || backend != business.AuditBackendWarehouse || storage != business.AuditStorage(fake) {
		t.Fatalf("%v %v %v", backend, storage, err)
	}
	if got != config {
		t.Fatalf("the builder was given %+v, want the group's names %+v", got, config)
	}
}

// The tee copies audit_events rows and the warehouse writes none: the pair is
// refused at startup, by name, before anything is built.
func TestSelectAuditStorageRefusesTheTeeWithTheWarehouse(t *testing.T) {
	built := false
	_, _, err := selectAuditStorage(auditstorage.Config{Backend: business.AuditBackendWarehouse, Dataset: "d", Table: "t"}, auditSinkBoth,
		func(auditstorage.Config) (business.AuditStorage, error) {
			built = true
			return fakeWarehouseAuditStorage{}, nil
		})
	if err == nil || !strings.Contains(err.Error(), "AUDIT_SINK=both") || !strings.Contains(err.Error(), "AUDIT_STORAGE_BACKEND=warehouse") {
		t.Fatalf("err = %v, want a refusal naming both settings", err)
	}
	if built {
		t.Fatal("the storage was built for a combination that is refused")
	}
}

// Until the warehouse AuditStorage exists in this build, `warehouse` is refused
// at startup with a message that says why and what to set instead.
func TestWarehouseBackendIsRefusedAtStartupUntilItsStorageLands(t *testing.T) {
	_, _, err := selectAuditStorage(auditstorage.Config{Backend: business.AuditBackendWarehouse, Dataset: "d", Table: "t"}, auditSinkPostgres, buildWarehouseAuditStorage)
	if !errors.Is(err, errWarehouseAuditStorageUnavailable) {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(err.Error(), "AUDIT_STORAGE_BACKEND=warehouse") || !strings.Contains(err.Error(), "postgres") {
		t.Fatalf("the refusal must name the setting and what to use instead: %v", err)
	}
}

func TestSelectAuditStorageRefusesABuilderThatReturnsNothing(t *testing.T) {
	_, _, err := selectAuditStorage(auditstorage.Config{Backend: business.AuditBackendWarehouse, Dataset: "d", Table: "t"}, auditSinkPostgres,
		func(auditstorage.Config) (business.AuditStorage, error) { return nil, nil })
	if err == nil {
		t.Fatal("a warehouse backend with no storage is refused")
	}
}
