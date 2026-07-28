package infra

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"

	"accounts/pkg/business"
)

const waitlistColumns = `
	id, normalized_email, name, company, use_case, state,
	verification_token_hash, verification_expires_at, verification_sent_at,
	source, campaign, referrer, referral_code, referred_by,
	marketing_consent, consent_policy_version, admin_notes, tags, created_at,
	verified_at, approved_at, invited_at, converted_at, unsubscribed_at,
	converted_user_id, converted_org_id`

func (s *PostgresStore) GetWaitlistReferralID(ctx context.Context, code string) (string, error) {
	var id string
	err := s.getQueryExecutor(ctx).QueryRow(ctx,
		`SELECT id FROM waitlist_entries WHERE referral_code = $1`, code,
	).Scan(&id)
	if err == pgx.ErrNoRows {
		return "", nil
	}
	return id, err
}

func (s *PostgresStore) UpsertWaitlistEntry(
	ctx context.Context,
	input *business.WaitlistEntry,
	cooldown time.Duration,
) (*business.WaitlistEntry, bool, error) {
	existing, err := s.getWaitlistEntry(ctx, "normalized_email", input.Email)
	if err != nil {
		return nil, false, err
	}
	now := time.Now()
	if existing != nil {
		if existing.State != "pending" {
			return existing, false, nil
		}
		if existing.VerificationSentAt != nil && now.Sub(*existing.VerificationSentAt) < cooldown {
			return existing, false, nil
		}
		_, err = s.getQueryExecutor(ctx).Exec(ctx, `
			UPDATE waitlist_entries
			SET verification_token_hash = $2,
			    verification_expires_at = $3,
			    verification_sent_at = $4,
			    name = COALESCE(NULLIF($5, ''), name),
			    company = COALESCE(NULLIF($6, ''), company),
			    use_case = COALESCE(NULLIF($7, ''), use_case)
			WHERE id = $1`,
			existing.ID,
			input.VerificationTokenHash,
			input.VerificationExpiresAt,
			now,
			input.Name,
			input.Company,
			input.UseCase,
		)
		if err != nil {
			return nil, false, err
		}
		existing.VerificationTokenHash = input.VerificationTokenHash
		existing.VerificationExpiresAt = input.VerificationExpiresAt
		existing.VerificationSentAt = &now
		return existing, true, nil
	}

	input.VerificationSentAt = &now
	_, err = s.getQueryExecutor(ctx).Exec(ctx, `
		INSERT INTO waitlist_entries (
			id, normalized_email, name, company, use_case, state,
			verification_token_hash, verification_expires_at, verification_sent_at,
			source, campaign, referrer, referral_code, referred_by,
			marketing_consent, consent_policy_version, verified_at
		)
		VALUES (
			$1, $2, $3, $4, $5, $6, $7, $8, $9,
			$10, $11, $12, $13, $14, $15, $16, $17
		)`,
		input.ID,
		input.Email,
		input.Name,
		input.Company,
		input.UseCase,
		input.State,
		nilIfEmpty(input.VerificationTokenHash),
		input.VerificationExpiresAt,
		input.VerificationSentAt,
		input.Source,
		input.Campaign,
		input.Referrer,
		input.ReferralCode,
		nilIfEmpty(input.ReferredBy),
		input.MarketingConsent,
		input.ConsentPolicyVersion,
		input.VerifiedAt,
	)
	return input, input.State == "pending", err
}

func (s *PostgresStore) VerifyWaitlistEntry(
	ctx context.Context,
	tokenHash string,
	now time.Time,
) (*business.WaitlistEntry, error) {
	entry, err := s.getWaitlistEntry(ctx, "verification_token_hash", tokenHash)
	if err != nil || entry == nil {
		return entry, err
	}
	if entry.VerificationExpiresAt == nil || now.After(*entry.VerificationExpiresAt) {
		return nil, business.ErrWaitlistTokenExpired
	}
	if entry.State == "pending" {
		_, err = s.getQueryExecutor(ctx).Exec(ctx, `
			UPDATE waitlist_entries
			SET state = 'verified', verified_at = COALESCE(verified_at, $2)
			WHERE id = $1 AND state = 'pending'`,
			entry.ID, now)
		if err != nil {
			return nil, err
		}
		entry.State = "verified"
		entry.VerifiedAt = &now
	}
	return entry, nil
}

func (s *PostgresStore) ListWaitlistEntries(
	ctx context.Context,
	state, query, source, campaign string,
	pageSize int32,
	pageToken string,
) ([]*business.WaitlistEntry, string, error) {
	if pageSize <= 0 {
		pageSize = 50
	}
	sql := `SELECT ` + waitlistColumns + ` FROM waitlist_entries
		WHERE ($1 = '' OR state = $1)
		  AND ($2 = '' OR normalized_email ILIKE '%' || $2 || '%'
		       OR name ILIKE '%' || $2 || '%' OR company ILIKE '%' || $2 || '%')
		  AND ($3 = '' OR source = $3)
		  AND ($4 = '' OR campaign = $4)
		  AND ($5 = '' OR created_at < $5::timestamptz)
		ORDER BY created_at DESC
		LIMIT $6`
	rows, err := s.getQueryExecutor(ctx).Query(
		ctx, sql, state, query, source, campaign, pageToken, pageSize,
	)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	var entries []*business.WaitlistEntry
	for rows.Next() {
		entry, scanErr := scanWaitlistEntry(rows)
		if scanErr != nil {
			return nil, "", scanErr
		}
		entries = append(entries, entry)
	}
	next := ""
	if int32(len(entries)) == pageSize {
		next = entries[len(entries)-1].CreatedAt.Format(time.RFC3339Nano)
	}
	return entries, next, rows.Err()
}

func (s *PostgresStore) UpdateWaitlistState(
	ctx context.Context,
	id, state, notes string,
	tags []string,
	now time.Time,
) (*business.WaitlistEntry, error) {
	timestampColumn := map[string]string{
		"approved":     "approved_at",
		"invited":      "invited_at",
		"unsubscribed": "unsubscribed_at",
	}[state]
	if state == "rejected" {
		row := s.getQueryExecutor(ctx).QueryRow(ctx, `
			UPDATE waitlist_entries
			SET state = 'rejected',
			    admin_notes = CASE WHEN $2 = '' THEN admin_notes ELSE $2 END,
			    tags = CASE WHEN $3::text[] IS NULL THEN tags ELSE $3 END
			WHERE id = $1
			  AND state NOT IN ('converted', 'unsubscribed')
			RETURNING `+waitlistColumns,
			id, notes, tags)
		entry, err := scanWaitlistEntry(row)
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return entry, err
	}
	if timestampColumn == "" {
		return nil, nil
	}
	query := `UPDATE waitlist_entries
		SET state = $2, ` + timestampColumn + ` = COALESCE(` + timestampColumn + `, $3),
		    admin_notes = CASE WHEN $4 = '' THEN admin_notes ELSE $4 END,
		    tags = CASE WHEN $5::text[] IS NULL THEN tags ELSE $5 END
		WHERE id = $1
		  AND state NOT IN ('converted', 'unsubscribed')
		RETURNING ` + waitlistColumns
	row := s.getQueryExecutor(ctx).QueryRow(ctx, query, id, state, now, notes, tags)
	entry, err := scanWaitlistEntry(row)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	return entry, err
}

func (s *PostgresStore) GetWaitlistStateByEmail(ctx context.Context, email string) (string, error) {
	var state string
	err := s.getQueryExecutor(ctx).QueryRow(ctx,
		`SELECT state FROM waitlist_entries WHERE normalized_email = LOWER(BTRIM($1))`,
		email,
	).Scan(&state)
	if err == pgx.ErrNoRows {
		return "", nil
	}
	return state, err
}

func (s *PostgresStore) ConvertWaitlistEntry(
	ctx context.Context,
	email, userID, orgID string,
	now time.Time,
) (*business.WaitlistEntry, error) {
	row := s.getQueryExecutor(ctx).QueryRow(ctx, `
		UPDATE waitlist_entries
		SET state = 'converted',
		    converted_at = COALESCE(converted_at, $4),
		    converted_user_id = COALESCE(converted_user_id, $2),
		    converted_org_id = COALESCE(converted_org_id, $3)
		WHERE normalized_email = LOWER(BTRIM($1))
		  AND state NOT IN ('rejected', 'unsubscribed')
		RETURNING `+waitlistColumns,
		email, userID, nilIfEmpty(orgID), now)
	entry, err := scanWaitlistEntry(row)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	return entry, err
}

func (s *PostgresStore) getWaitlistEntry(
	ctx context.Context,
	column, value string,
) (*business.WaitlistEntry, error) {
	row := s.getQueryExecutor(ctx).QueryRow(
		ctx, `SELECT `+waitlistColumns+` FROM waitlist_entries WHERE `+column+` = $1 FOR UPDATE`,
		value,
	)
	entry, err := scanWaitlistEntry(row)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	return entry, err
}

type waitlistScanner interface {
	Scan(dest ...any) error
}

func scanWaitlistEntry(scanner waitlistScanner) (*business.WaitlistEntry, error) {
	var entry business.WaitlistEntry
	var tokenHash, referredBy, convertedUserID, convertedOrgID *string
	err := scanner.Scan(
		&entry.ID,
		&entry.Email,
		&entry.Name,
		&entry.Company,
		&entry.UseCase,
		&entry.State,
		&tokenHash,
		&entry.VerificationExpiresAt,
		&entry.VerificationSentAt,
		&entry.Source,
		&entry.Campaign,
		&entry.Referrer,
		&entry.ReferralCode,
		&referredBy,
		&entry.MarketingConsent,
		&entry.ConsentPolicyVersion,
		&entry.AdminNotes,
		&entry.Tags,
		&entry.CreatedAt,
		&entry.VerifiedAt,
		&entry.ApprovedAt,
		&entry.InvitedAt,
		&entry.ConvertedAt,
		&entry.UnsubscribedAt,
		&convertedUserID,
		&convertedOrgID,
	)
	if err != nil {
		return nil, err
	}
	if tokenHash != nil {
		entry.VerificationTokenHash = *tokenHash
	}
	if referredBy != nil {
		entry.ReferredBy = *referredBy
	}
	if convertedUserID != nil {
		entry.ConvertedUserID = *convertedUserID
	}
	if convertedOrgID != nil {
		entry.ConvertedOrganizationID = *convertedOrgID
	}
	return &entry, nil
}
