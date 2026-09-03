package main

import (
	"testing"

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
