package infra

import (
	"net/url"
	"os"
	"strings"
	"testing"
)

func clearDatabaseEnvironment(t *testing.T) {
	t.Helper()
	for _, entry := range os.Environ() {
		key, value, ok := strings.Cut(entry, "=")
		if ok && (strings.HasPrefix(key, "PG") || key == "POSTGRES_TOKEN_FILE") {
			if err := os.Unsetenv(key); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.Setenv(key, value) })
		}
	}
}
func proxyURL() string {
	return "postgresql://fixture@/accounts?" + url.Values{"host": {"/db/r/instance"}, "port": {"5432"}, "sslmode": {"disable"}, "passfile": {"/dev/null"}}.Encode()
}

func TestAccountsExplicitProxyTransport(t *testing.T) {
	clearDatabaseEnvironment(t)
	good := proxyURL()
	cfg, err := parseDatabaseTransport(good, "local-identity-proxy", false)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ConnConfig.User != "fixture" || cfg.ConnConfig.Host != "/db/r/instance" || cfg.ConnConfig.Password != "" || cfg.ConnConfig.TLSConfig != nil {
		t.Fatal("proxy capability changed")
	}
	for name, connection := range map[string]string{
		"TCP":             strings.Replace(good, "@/", "@localhost/", 1),
		"empty password":  strings.Replace(good, "fixture@", "fixture:@", 1),
		"password":        strings.Replace(good, "fixture@", "fixture:secret@", 1),
		"relative socket": strings.Replace(good, "%2Fdb%2Fr%2Finstance", "relative", 1),
		"duplicate port":  good + "&port=5433", "endpoint override": good + "&hostaddr=127.0.0.1",
		"role override": good + "&role=app_control_plane", "options": good + "&options=-c+role%3Dapp_control_plane",
		"passfile":     strings.Replace(good, "%2Fdev%2Fnull", "%2Ftmp%2Fpasswords", 1),
		"missing port": strings.Replace(good, "port=5432&", "", 1),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := parseDatabaseTransport(connection, "local-identity-proxy", false); err == nil {
				t.Fatal("ambiguous proxy binding accepted")
			}
		})
	}
	if _, err := parseDatabaseTransport(good, "local-identity-proxy", true); err == nil {
		t.Fatal("shared token hook accepted")
	}
	t.Run("ambient PG", func(t *testing.T) {
		t.Setenv("PGHOST", "elsewhere")
		if _, err := parseDatabaseTransport(good, "local-identity-proxy", false); err == nil {
			t.Fatal("ambient PG accepted")
		}
	})
	t.Run("shared token file", func(t *testing.T) {
		t.Setenv("POSTGRES_TOKEN_FILE", "/private/credential")
		if _, err := parseDatabaseTransport(good, "local-identity-proxy", false); err == nil {
			t.Fatal("shared token file accepted")
		}
	})
}

func TestAccountsDefaultAndVerifiedTransport(t *testing.T) {
	clearDatabaseEnvironment(t)
	legacy := "postgresql://fixture@localhost/accounts?sslmode=disable"
	if _, err := parseDatabaseTransport(legacy, "", false); err != nil {
		t.Fatal("legacy consumer default changed")
	}
	verified := "postgresql://fixture@database.example.invalid/accounts?sslmode=verify-full"
	if _, err := parseDatabaseTransport(verified, "verified-tls", false); err != nil {
		t.Fatal(err)
	}
	for _, connection := range []string{legacy, proxyURL(), verified + "&host=%2Fdb%2Fw", verified + "&user=other", verified + "&sslmode=disable", strings.Replace(verified, "verify-full", "require", 1)} {
		if _, err := parseDatabaseTransport(connection, "verified-tls", false); err == nil {
			t.Fatal("unverified TLS accepted")
		}
	}
	if _, err := parseDatabaseTransport(verified, "unknown", false); err == nil {
		t.Fatal("unknown transport accepted")
	}
	t.Setenv("ACCOUNTS_DATABASE_TRANSPORT", "local-identity-proxy")
	if got, err := DatabaseTransportProfile(); err != nil || got != "local-identity-proxy" {
		t.Fatal("configured profile not selected")
	}
}
