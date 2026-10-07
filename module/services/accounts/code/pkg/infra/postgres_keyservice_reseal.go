package infra

import (
	"context"
	"fmt"
	"sort"

	"accounts/pkg/business"
	"accounts/pkg/keyservice"
)

// Re-sealing every stored credential under a newly selected key service.
//
// A cutover has two halves. Reads work from the first moment both backends are
// bound, because every envelope names the backend that sealed it. Withdrawing
// the outgoing backend is the half that needs this verb: until nothing
// references it, removing it from the deployment turns every value it sealed
// into a credential nobody can read.
//
// So the verb does two things that are really one: it re-seals what the outgoing
// backend holds, and it reports what is left. The report is the answer to "is it
// safe to remove the previous backend yet", and it is the only honest way to ask
// — a sweep that returned only a count of what it changed cannot distinguish
// "finished" from "failed on the first row".

// ResealOutcome is what the sweep did to one enveloped column, and what is still
// sealed under something other than the selected backend.
type ResealOutcome struct {
	Table, Column string
	Resealed      int
	// Remaining counts rows per backend tag that the selected backend did not
	// seal. An empty map is what makes withdrawing the previous backend safe.
	Remaining map[string]int
}

// enveloped is one column holding sealed credentials, and how to rebuild the
// associated data each of its rows was sealed with.
//
// The purpose is per row for most of these: it binds a ciphertext to the row it
// belongs to, so copying one between rows no longer opens. Re-sealing therefore
// has to reconstruct exactly the purpose the value was sealed under, which is
// why each entry names the column the purpose is derived from rather than
// assuming the primary key.
type enveloped struct {
	table, column string
	// idColumn identifies a row for the compare-and-swap write. Not every table
	// has an `id`: webauthn_credentials is keyed by the device it belongs to.
	idColumn string
	// keyColumn supplies the purpose's binding. Empty where the purpose is a
	// constant.
	keyColumn string
	// where narrows to the rows that actually carry an envelope, beyond the
	// not-null and not-empty check every entry gets.
	where   string
	purpose func(key string) string
}

// envelopedColumns is the inventory. TestResealCoversEveryDeclaredPurpose
// derives the purpose set from pkg/business's own source and fails when one is
// missing here, because this list being complete is not something review can
// see: a purpose nobody re-seals keeps the outgoing backend referenced forever,
// and the sweep still reports success.
var envelopedColumns = []enveloped{
	{
		table: "mfa_devices", column: "secret_encrypted",
		where:   "device_type = 'totp'",
		purpose: func(string) string { return business.MFATOTPPurpose },
	},
	{
		table: "webauthn_credentials", column: "credential_encrypted",
		idColumn: "device_id",
		purpose:  func(string) string { return business.WebAuthnCredentialPurpose },
	},
	{
		// Ceremonies expire within minutes, so this column empties itself. It is
		// swept anyway: "nothing references the outgoing backend" has to be true
		// of every column, and an operator should not have to know which tables
		// drain on their own to read the report.
		table: "webauthn_ceremonies", column: "session_data_encrypted",
		purpose: func(string) string { return business.WebAuthnSessionPurpose },
	},
	{
		table: "webhook_subscriptions", column: "secret_encrypted",
		keyColumn: "id", purpose: business.WebhookSecretPurpose,
	},
	{
		// The rotation overlap's outgoing secret, under the same purpose as the
		// current one: both are bound to the subscription, not to the slot.
		table: "webhook_subscriptions", column: "previous_secret_encrypted",
		keyColumn: "id", purpose: business.WebhookSecretPurpose,
	},
	{
		table: "datasource_sources", column: "credential_secret_ref",
		keyColumn: "id", purpose: business.DatasourceConnectorSecretPurpose,
	},
	{
		table: "datasource_sources", column: "webhook_secret_ref",
		keyColumn: "id", purpose: business.DatasourceWebhookSecretPurpose,
	},
	{
		// Bound to the org, not the row: one provider per org, and org_id is the
		// table's stable identity.
		table: "org_identity_providers", column: "client_secret_ref",
		keyColumn: "org_id", purpose: business.OrgIdentityProviderSecretPurpose,
	},
	{
		table: "connector_credentials", column: "secret_encrypted",
		keyColumn: "source_id", purpose: business.ConnectorSecretPurpose,
	},
}

// ResealEnvelopes rewrites every enveloped column under the backend the key
// service currently seals with, and reports what is still sealed under anything
// else.
//
// It is restart-safe and idempotent: a value the selected backend already sealed
// is skipped without a key-service call, and each write is a compare-and-swap on
// the exact envelope that was read, so two replicas running this at once cannot
// lose a row and an interrupted run simply resumes.
//
// Key-service calls happen outside the database transactions. Each one is a
// network round trip to Vault or a cloud key service, and holding a transaction
// open across thousands of them would pin a connection and a snapshot for the
// length of the whole sweep.
func (s *PostgresStore) ResealEnvelopes(ctx context.Context, cipher *keyservice.Cipher) ([]ResealOutcome, error) {
	if cipher == nil {
		return nil, fmt.Errorf("re-seal requires a key service")
	}
	outcomes := make([]ResealOutcome, 0, len(envelopedColumns))
	for _, column := range envelopedColumns {
		outcome, err := s.resealColumn(ctx, cipher, column)
		if err != nil {
			return outcomes, fmt.Errorf("re-seal %s.%s: %w", column.table, column.column, err)
		}
		outcomes = append(outcomes, outcome)
	}
	return outcomes, nil
}

type envelopedRow struct {
	id, key, stored string
}

func (s *PostgresStore) resealColumn(
	ctx context.Context,
	cipher *keyservice.Cipher,
	column enveloped,
) (ResealOutcome, error) {
	outcome := ResealOutcome{Table: column.table, Column: column.column, Remaining: map[string]int{}}

	keyExpression := "''"
	if column.keyColumn != "" {
		keyExpression = column.keyColumn + "::text"
	}
	idColumn := column.idColumn
	if idColumn == "" {
		idColumn = "id"
	}
	where := column.column + " IS NOT NULL AND " + column.column + " <> ''"
	if column.where != "" {
		where += " AND " + column.where
	}
	// Control plane for both halves. Credential custody is platform-wide by
	// definition: the sweep covers every tenant's rows, so there is no org or
	// user scope it could run under, and enumerating orgs to re-seal a key would
	// be the same authority spelled less directly. These seven tables each carry
	// an app_control_plane_explicit_rows policy with SELECT and UPDATE granted
	// to that role. No caller-supplied input reaches the predicate: the table,
	// column and filter are all from the inventory above.
	var rows []envelopedRow
	if err := s.WithControlPlane(ctx, func(ctx context.Context) error {
		//nolint:gosec // identifiers come from envelopedColumns, never from a caller
		result, err := s.getQueryExecutor(ctx).Query(ctx, fmt.Sprintf(
			`SELECT %s::text, %s, %s FROM %s WHERE %s`,
			idColumn, keyExpression, column.column, column.table, where))
		if err != nil {
			return err
		}
		defer result.Close()
		for result.Next() {
			var row envelopedRow
			if err := result.Scan(&row.id, &row.key, &row.stored); err != nil {
				return err
			}
			rows = append(rows, row)
		}
		return result.Err()
	}); err != nil {
		return outcome, err
	}

	for _, row := range rows {
		// Pre-envelope plaintext belongs to the legacy migrations, which run
		// before this and own the base32 and empty-secret cases. Re-sealing one
		// here would seal it under the wrong purpose-bound shape.
		if !keyservice.IsEnvelopeFraming(row.stored) {
			continue
		}
		purpose := column.purpose(row.key)
		resealed, changed, err := cipher.Reseal(ctx, purpose, row.stored)
		if err != nil {
			// Record what could not be moved rather than stopping: one
			// unreadable row must not hide the state of every other column, and
			// the remaining count is what the operator acts on.
			if envelope, parseErr := keyservice.ParseEnvelope(row.stored); parseErr == nil {
				outcome.Remaining[envelope.Backend]++
			} else {
				outcome.Remaining["unparseable"]++
			}
			continue
		}
		if !changed {
			continue
		}
		if err := s.WithControlPlane(ctx, func(ctx context.Context) error {
			//nolint:gosec // identifiers come from envelopedColumns, never from a caller
			tag, err := s.getQueryExecutor(ctx).Exec(ctx, fmt.Sprintf(
				`UPDATE %s SET %s = $2 WHERE %s = $1 AND %s = $3`,
				column.table, column.column, idColumn, column.column),
				row.id, resealed, row.stored)
			if err != nil {
				return err
			}
			if tag.RowsAffected() == 1 {
				outcome.Resealed++
			}
			return nil
		}); err != nil {
			return outcome, err
		}
	}
	return outcome, nil
}

// ResealReport renders the outcomes for a log line, newest-first by what is
// still outstanding, so the one number an operator needs — is anything still
// sealed under the outgoing backend — is not buried in a per-table list.
func ResealReport(outcomes []ResealOutcome) (resealed int, remaining map[string]int, detail []string) {
	remaining = map[string]int{}
	for _, outcome := range outcomes {
		resealed += outcome.Resealed
		for tag, count := range outcome.Remaining {
			remaining[tag] += count
			detail = append(detail, fmt.Sprintf("%s.%s=%d under %s", outcome.Table, outcome.Column, count, tag))
		}
	}
	sort.Strings(detail)
	return resealed, remaining, detail
}
