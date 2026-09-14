package infra

import (
	"errors"
	"os"

	codefly "github.com/codefly-dev/sdk-go"
	scopedpostgres "github.com/codefly-dev/service-postgres/libs/go"
	"github.com/jackc/pgx/v5/pgxpool"
)

// DatabaseTransportProfile selects an explicitly reviewed deployment transport.
// Empty retains the legacy Codefly projection behavior for existing consumers;
// hosted custody requires one of the two explicit profiles.
func DatabaseTransportProfile() (string, error) {
	value, _ := codefly.For(codefly.Context()).WorkspaceValue("security", "ACCOUNTS_DATABASE_TRANSPORT")
	if value == "" {
		value = os.Getenv("ACCOUNTS_DATABASE_TRANSPORT")
	}
	switch value {
	case "", "verified-tls", "local-identity-proxy":
		return value, nil
	}
	return "", errors.New("unsupported Accounts database transport")
}

// databaseConnectionProfile preserves this module's intentional legacy default
// and its prohibition on URL role selection. Token-file projection is a local
// composition policy; the primitive only sees the actual provider/hook.
func databaseConnectionProfile(profile string) (scopedpostgres.ConnectionProfile, error) {
	switch profile {
	case "", "verified-tls", "local-identity-proxy":
	default:
		return scopedpostgres.ConnectionProfile{}, errors.New("unsupported Accounts database transport")
	}
	if profile == "local-identity-proxy" && os.Getenv(databaseTokenFileEnv) != "" {
		return scopedpostgres.ConnectionProfile{}, errors.New("local database proxy cannot use a token file")
	}
	return scopedpostgres.ConnectionProfile{Transport: scopedpostgres.ConnectionTransport(profile)}, nil
}

// The intentional legacy/control-plane pool uses the returned driver config
// directly. Request pools use the same policy inside service-postgres Open.
func parseDatabaseTransport(connection, profile string, tokenHook bool) (*pgxpool.Config, error) {
	policy, err := databaseConnectionProfile(profile)
	if err != nil {
		return nil, err
	}
	return scopedpostgres.ParseConnection(connection, policy, tokenHook)
}
