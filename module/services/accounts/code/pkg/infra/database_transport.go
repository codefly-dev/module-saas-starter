package infra

import (
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	codefly "github.com/codefly-dev/sdk-go"
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

// parseDatabaseTransport opens no pool and changes no role. Fixed role selection
// and verified physical identities remain owned by the existing store factory.
func parseDatabaseTransport(connection, profile string, tokenHook bool) (*pgxpool.Config, error) {
	invalid := errors.New("invalid Accounts database transport binding")
	if profile == "" {
		return pgxpool.ParseConfig(connection)
	}
	if profile != "verified-tls" && profile != "local-identity-proxy" {
		return nil, invalid
	}
	for _, entry := range os.Environ() {
		if strings.HasPrefix(entry, "PG") {
			return nil, invalid
		}
	}
	u, err := url.Parse(connection)
	if err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") || u.Opaque != "" || u.Fragment != "" || u.User == nil || u.User.Username() == "" || !strings.HasPrefix(u.Path, "/") || u.Path == "/" || strings.ContainsAny(strings.TrimPrefix(u.Path, "/"), "/\x00") {
		return nil, invalid
	}
	q, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return nil, invalid
	}
	for _, values := range q {
		if len(values) != 1 {
			return nil, invalid
		}
	}
	if profile == "local-identity-proxy" {
		if u.Host != "" || q.Get("sslmode") != "disable" || q.Get("passfile") != "/dev/null" || tokenHook || os.Getenv("POSTGRES_TOKEN_FILE") != "" {
			return nil, invalid
		}
		if _, present := u.User.Password(); present {
			return nil, invalid
		}
		for key := range q {
			switch key {
			case "host", "port", "sslmode", "passfile":
			default:
				return nil, invalid
			}
		}
		socket := q.Get("host")
		if !filepath.IsAbs(socket) || filepath.Clean(socket) != socket || socket == "/" || strings.ContainsAny(socket, ",\x00\r\n") || strings.Contains(socket, ".s.PGSQL.") {
			return nil, invalid
		}
		port, err := strconv.ParseUint(q.Get("port"), 10, 16)
		if err != nil || port == 0 || len(socket+"/.s.PGSQL."+q.Get("port")) > 103 {
			return nil, invalid
		}
		cfg, err := pgxpool.ParseConfig(connection)
		if err != nil || cfg.ConnConfig.Host != socket || cfg.ConnConfig.Port != uint16(port) || cfg.ConnConfig.Database != strings.TrimPrefix(u.Path, "/") || cfg.ConnConfig.User != u.User.Username() || cfg.ConnConfig.Password != "" || cfg.ConnConfig.TLSConfig != nil || len(cfg.ConnConfig.Fallbacks) != 0 || len(cfg.ConnConfig.RuntimeParams) != 0 {
			return nil, invalid
		}
		return cfg, nil
	}
	if u.Hostname() == "" || strings.Contains(u.Hostname(), ",") || q.Get("sslmode") != "verify-full" {
		return nil, invalid
	}
	for key := range q {
		switch key {
		case "host", "hostaddr", "port", "user", "password", "dbname", "database", "service", "servicefile", "options", "role":
			return nil, invalid
		}
	}
	cfg, err := pgxpool.ParseConfig(connection)
	if err != nil || cfg.ConnConfig.Host != u.Hostname() || cfg.ConnConfig.Database != strings.TrimPrefix(u.Path, "/") || cfg.ConnConfig.User != u.User.Username() || cfg.ConnConfig.TLSConfig == nil || cfg.ConnConfig.TLSConfig.InsecureSkipVerify || cfg.ConnConfig.TLSConfig.ServerName != u.Hostname() {
		return nil, invalid
	}
	for _, fallback := range cfg.ConnConfig.Fallbacks {
		if fallback.TLSConfig == nil || fallback.TLSConfig.InsecureSkipVerify || fallback.TLSConfig.ServerName != u.Hostname() {
			return nil, invalid
		}
	}
	return cfg, nil
}
