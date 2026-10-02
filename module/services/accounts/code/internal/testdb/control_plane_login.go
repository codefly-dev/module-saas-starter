package testdb

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"regexp"

	codefly "github.com/codefly-dev/sdk-go"
	"github.com/jackc/pgx/v5"
)

// Helpers for adversarial login-authority tests. The production-shaped fixture
// gets its declared runtime-logins only from the published Postgres agent.

const (
	storeService           = "store"
	postgresConfiguration  = "postgres"
	ownerConnectionKey     = "owner-connection"
	readWriteConnectionKey = "read-write-connection"
)

// loginName bounds what CreateLogin interpolates into role DDL.
var loginName = regexp.MustCompile(`^[a-z_][a-z0-9_]{0,62}$`)

// CreateLogin provisions — or re-provisions, since a persisted local store keeps
// its roles between runs — a login in the store's database the way the Postgres
// agent provisions its read-write login: LOGIN and NOINHERIT with no elevated
// attribute, CONNECT on the database, USAGE on public, membership in exactly
// roles, and the first of them as its session default. It returns a connection
// string for the login, built from the store's read-write connection with a
// fresh random password. It uses the store's local owner connection, so it works
// only against a local, password-authenticated store.
func CreateLogin(ctx context.Context, name string, roles ...string) (string, error) {
	for _, role := range append([]string{name}, roles...) {
		if !loginName.MatchString(role) {
			return "", fmt.Errorf("%q is not a plain role name", role)
		}
	}
	owner, err := storeSecret(ctx, ownerConnectionKey)
	if err != nil {
		return "", fmt.Errorf("the store's owner connection: %w", err)
	}
	readWrite, err := storeSecret(ctx, readWriteConnectionKey)
	if err != nil {
		return "", fmt.Errorf("the store's read-write connection: %w", err)
	}
	connection, err := url.Parse(readWrite)
	if err != nil {
		return "", fmt.Errorf("parse the store's read-write connection: %w", err)
	}
	if _, hasPassword := connection.User.Password(); !hasPassword {
		return "", errors.New("CreateLogin needs a password-authenticated local store")
	}
	secret := make([]byte, 24)
	if _, err := rand.Read(secret); err != nil {
		return "", err
	}
	password := hex.EncodeToString(secret)

	conn, err := pgx.Connect(ctx, owner)
	if err != nil {
		return "", fmt.Errorf("connect as the store owner: %w", err)
	}
	defer conn.Close(context.Background()) //nolint:errcheck // closing a test fixture connection
	tx, err := conn.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // a no-op after Commit

	// The Postgres agent takes this transaction lock around every role and grant
	// change it makes, its own reconcile included; without it the two collide on
	// the role catalog and one aborts with "tuple concurrently updated".
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('codefly-runtime-access:' || current_database()))`); err != nil {
		return "", fmt.Errorf("take the runtime-access lock: %w", err)
	}

	var database string
	if err := tx.QueryRow(ctx, `SELECT current_database()`).Scan(&database); err != nil {
		return "", err
	}
	login := pgx.Identifier{name}.Sanitize()
	statements := []string{
		`DO $$ BEGIN
			IF NOT EXISTS (SELECT 1 FROM pg_catalog.pg_roles WHERE rolname = '` + name + `') THEN
				CREATE ROLE ` + login + `;
			END IF;
		END $$`,
		// The password is hex, so it needs no escaping inside the literal.
		`ALTER ROLE ` + login + ` WITH LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT NOREPLICATION NOBYPASSRLS PASSWORD '` + password + `'`,
		`REVOKE ALL PRIVILEGES ON DATABASE ` + pgx.Identifier{database}.Sanitize() + ` FROM ` + login,
		`GRANT CONNECT ON DATABASE ` + pgx.Identifier{database}.Sanitize() + ` TO ` + login,
		`GRANT USAGE ON SCHEMA public TO ` + login,
	}
	for _, statement := range statements {
		if _, err := tx.Exec(ctx, statement); err != nil {
			return "", fmt.Errorf("provision login %s: %w", name, err)
		}
	}
	rows, err := tx.Query(ctx, `
		SELECT granted.rolname::text
		FROM pg_catalog.pg_auth_members membership
		JOIN pg_catalog.pg_roles granted ON granted.oid = membership.roleid
		JOIN pg_catalog.pg_roles member ON member.oid = membership.member
		WHERE member.rolname = $1`, name)
	if err != nil {
		return "", err
	}
	previous, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return "", err
	}
	for _, role := range previous {
		if _, err := tx.Exec(ctx, `REVOKE `+pgx.Identifier{role}.Sanitize()+` FROM `+login); err != nil {
			return "", fmt.Errorf("revoke %s from %s: %w", role, name, err)
		}
	}
	for _, role := range roles {
		if _, err := tx.Exec(ctx, `GRANT `+pgx.Identifier{role}.Sanitize()+` TO `+login); err != nil {
			return "", fmt.Errorf("grant %s to %s: %w", role, name, err)
		}
	}
	defaultRole := `ALTER ROLE ` + login + ` RESET role`
	if len(roles) > 0 {
		defaultRole = `ALTER ROLE ` + login + ` SET role = '` + roles[0] + `'`
	}
	if _, err := tx.Exec(ctx, defaultRole); err != nil {
		return "", fmt.Errorf("set the session default of %s: %w", name, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return "", err
	}

	connection.User = url.UserPassword(name, password)
	return connection.String(), nil
}

func storeSecret(ctx context.Context, key string) (string, error) {
	return codefly.For(ctx).Service(storeService).Secret(postgresConfiguration, key)
}
