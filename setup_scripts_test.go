package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestProviderSetupScriptsExposeHelp(t *testing.T) {
	// stripe and resend are non-writing shims; their contracts are
	// TestStripeSetupIsNonWritingShim and TestResendSetupIsNonWritingShim.
	for _, provider := range []string{
		"workos", "posthog", "sentry", "otel", "turnstile",
	} {
		t.Run(provider, func(t *testing.T) {
			command := exec.Command("bash", setupScript(t, provider), "--help")
			output, err := command.CombinedOutput()
			if err != nil {
				t.Fatalf("%s --help: %v\n%s", provider, err, output)
			}
			if !strings.Contains(string(output), "local-dogfood") &&
				!strings.Contains(string(output), "OpenTelemetry") {
				t.Fatalf("%s help did not describe its dogfood setup", provider)
			}
		})
	}
}

func TestProviderSetupScriptsInstallSecretSafeIndependentConfigurations(t *testing.T) {
	workspace := newSetupWorkspace(t)
	bin := filepath.Join(t.TempDir(), "bin")
	mustMkdir(t, bin)
	codefly := filepath.Join(bin, "codefly")
	mustWrite(t, codefly, "#!/usr/bin/env bash\nif [[ \"$1\" == endpoint ]]; then [[ \"$2 $3 $4\" == \"frontend --type http\" ]] || exit 64; printf 'http://localhost:42152\\n'; exit 0; fi\nexit 0\n", 0o755)
	path := bin + string(os.PathListSeparator) + os.Getenv("PATH")

	cases := []struct {
		name    string
		args    []string
		secrets []string
		public  string
		secret  string
	}{
		{
			name: "posthog",
			args: []string{
				"--project-key-file", secretFile(t, "POSTHOG_PROJECT_API_KEY=phc_codeflyfixture"),
				"--personal-key-file", secretFile(t, "POSTHOG_PERSONAL_API_KEY=phx_codeflyfixture"),
				"--project-id", "42",
				"--host", "http://localhost:8000",
				"--api-host", "http://localhost:8001",
				"--skip-remote-validation",
			},
			secrets: []string{"phx_codeflyfixture"},
			public:  "product-analytics.env", secret: "product-analytics.secret.env",
		},
		{
			name: "sentry",
			args: []string{
				"--env-file", secretFile(t, strings.Join([]string{
					"SENTRY_AUTH_TOKEN=sntrys_codefly_fixture_token",
					"SENTRY_DSN=https://public@sentry.example/42",
				}, "\n")),
				"--org", "codefly", "--project", "starter",
				"--skip-remote-validation",
			},
			secrets: []string{"sntrys_codefly_fixture_token"},
			public:  "error-tracking.env", secret: "error-tracking.secret.env",
		},
		{
			name:   "otel",
			args:   []string{"--debug"},
			public: "observability.env", secret: "observability.secret.env",
		},
		{
			name:    "turnstile",
			args:    []string{"--fixture", "pass"},
			secrets: []string{"1x0000000000000000000000000000000AA"},
			public:  "abuse-protection.env", secret: "abuse-protection.secret.env",
		},
	}

	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			args := append(test.args, "--workspace", workspace, "--skip-doctor")
			commandArgs := append([]string{setupScript(t, test.name)}, args...)
			command := exec.Command("bash", commandArgs...)
			command.Env = append(os.Environ(), "PATH="+path)
			output, err := command.CombinedOutput()
			if err != nil {
				t.Fatalf("%s setup: %v\n%s", test.name, err, output)
			}
			for _, secret := range test.secrets {
				if strings.Contains(string(output), secret) {
					t.Fatalf("%s printed a secret", test.name)
				}
			}
			assertPrivateFile(t, filepath.Join(workspace, "configurations/local-dogfood", test.public))
			assertPrivateFile(t, filepath.Join(workspace, "configurations/local-dogfood", test.secret))
		})
	}
}

func TestWorkOSSetupUsesFrontendEntrypointWithoutPrintingSecret(t *testing.T) {
	workspace := newSetupWorkspace(t)
	bin := filepath.Join(t.TempDir(), "bin")
	mustMkdir(t, bin)
	codefly := filepath.Join(bin, "codefly")
	mustWrite(t, codefly, "#!/usr/bin/env bash\nprintf '%s\\n' \"$*\" >>\"$CODEFLY_TEST_LOG\"\nif [[ \"$1\" == endpoint ]]; then printf 'http://localhost:42152\\n'; fi\n", 0o755)
	logFile := filepath.Join(t.TempDir(), "codefly.log")
	apiKey := "sk_codeflyfixture"
	command := exec.Command(
		"bash",
		setupScript(t, "workos"),
		"--client-id", "client_01CODEFLY",
		"--api-key-file", secretFile(t, apiKey),
		"--workspace", workspace,
		"--skip-remote-validation",
		"--skip-doctor",
	)
	command.Env = append(
		os.Environ(),
		"PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"),
		"CODEFLY_TEST_LOG="+logFile,
	)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("workos setup: %v\n%s", err, output)
	}
	if strings.Contains(string(output), apiKey) {
		t.Fatal("workos setup printed its API key")
	}
	logged, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(logged), "endpoint frontend --type http") {
		t.Fatalf("workos setup did not resolve the frontend endpoint:\n%s", logged)
	}
	if !strings.Contains(string(output), "codefly run service --env local-dogfood") {
		t.Fatalf("workos setup did not print the module-entry run command:\n%s", output)
	}
	publicPath := filepath.Join(workspace, "configurations/local-dogfood", "identity.env")
	secretPath := filepath.Join(workspace, "configurations/local-dogfood", "identity.secret.env")
	assertPrivateFile(t, publicPath)
	assertPrivateFile(t, secretPath)
	public, err := os.ReadFile(publicPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(public), "IDENTITY_CLIENT_ID=client_01CODEFLY") {
		t.Fatalf("workos public configuration did not contain the client ID:\n%s", public)
	}
	secret, err := os.ReadFile(secretPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(secret), "IDENTITY_CLIENT_SECRET="+apiKey) {
		t.Fatal("workos secret configuration did not contain the API key")
	}
}

func TestStripeSetupIsNonWritingShim(t *testing.T) {
	script := setupScript(t, "stripe")

	// Every flag whose writing semantics moved to the provider plugin fails
	// closed with migration guidance rather than acting.
	for _, flag := range []string{
		"--provision-webhook", "--skip-remote-validation", "--webhook-origin",
		"--webhook-secret-file", "--force", "--workspace", "--skip-doctor",
	} {
		t.Run("removed"+flag, func(t *testing.T) {
			output, err := exec.Command("bash", script, flag, "unused").CombinedOutput()
			if err == nil {
				t.Fatalf("%s was accepted; the shim must reject it:\n%s", flag, output)
			}
			if !strings.Contains(string(output), "is removed") ||
				!strings.Contains(string(output), "codefly-dev/provider-stripe") {
				t.Fatalf("%s did not explain the migration:\n%s", flag, output)
			}
		})
	}

	// A test-mode key is classified without being printed, and the shim writes
	// no configuration into its working directory.
	for _, key := range []string{"sk_test_codeflyfixture", "rk_test_codeflyfixture"} {
		t.Run("accept/"+key, func(t *testing.T) {
			dir := t.TempDir()
			command := exec.Command("bash", script,
				"--api-key-file", secretFile(t, "STRIPE_API_KEY="+key))
			command.Dir = dir
			output, err := command.CombinedOutput()
			if err != nil {
				t.Fatalf("%s was refused: %v\n%s", key, err, output)
			}
			if strings.Contains(string(output), key) {
				t.Fatalf("shim printed the %s key", key)
			}
			entries, err := os.ReadDir(dir)
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != 0 {
				t.Fatalf("shim wrote %d file(s) into its working directory", len(entries))
			}
		})
	}

	t.Run("refuses live key", func(t *testing.T) {
		output, err := exec.Command("bash", script,
			"--api-key-file", secretFile(t, "STRIPE_API_KEY=sk_live_codeflyfixture")).CombinedOutput()
		if err == nil {
			t.Fatalf("shim accepted a live-mode key:\n%s", output)
		}
		if !strings.Contains(string(output), "live-mode key") {
			t.Fatalf("shim did not explain the live-key refusal:\n%s", output)
		}
	})

	t.Run("help points at the plugin", func(t *testing.T) {
		output, err := exec.Command("bash", script, "--help").CombinedOutput()
		if err != nil {
			t.Fatalf("stripe --help: %v\n%s", err, output)
		}
		if !strings.Contains(string(output), "codefly-dev/provider-stripe") {
			t.Fatalf("stripe --help did not point at the plugin:\n%s", output)
		}
	})
}

func TestResendSetupIsNonWritingShim(t *testing.T) {
	script := setupScript(t, "resend")

	// Every flag whose writing or remote semantics moved to the provider plugin
	// fails closed with migration guidance rather than acting.
	for _, flag := range []string{
		"--provision-webhook", "--skip-remote-validation", "--webhook-origin",
		"--webhook-secret-file", "--from", "--force", "--workspace", "--skip-doctor",
	} {
		t.Run("removed"+flag, func(t *testing.T) {
			output, err := exec.Command("bash", script, flag, "unused").CombinedOutput()
			if err == nil {
				t.Fatalf("%s was accepted; the shim must reject it:\n%s", flag, output)
			}
			if !strings.Contains(string(output), "is removed") ||
				!strings.Contains(string(output), "codefly-dev/provider-resend") {
				t.Fatalf("%s did not explain the migration:\n%s", flag, output)
			}
		})
	}

	// A well-formed key is classified without being printed, and the shim writes
	// no configuration into its working directory.
	t.Run("accept well-formed key", func(t *testing.T) {
		key := "re_codeflyfixture_ABC123xyz"
		dir := t.TempDir()
		command := exec.Command("bash", script,
			"--api-key-file", secretFile(t, "RESEND_API_KEY="+key))
		command.Dir = dir
		output, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("%s was refused: %v\n%s", key, err, output)
		}
		if strings.Contains(string(output), key) {
			t.Fatalf("shim printed the %s key", key)
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) != 0 {
			t.Fatalf("shim wrote %d file(s) into its working directory", len(entries))
		}
	})

	t.Run("refuses a malformed key", func(t *testing.T) {
		output, err := exec.Command("bash", script,
			"--api-key-file", secretFile(t, "RESEND_API_KEY=sk_live_notresend")).CombinedOutput()
		if err == nil {
			t.Fatalf("shim accepted a malformed key:\n%s", output)
		}
		if !strings.Contains(string(output), "well-formed Resend API key") {
			t.Fatalf("shim did not explain the malformed-key refusal:\n%s", output)
		}
	})

	t.Run("help points at the plugin", func(t *testing.T) {
		output, err := exec.Command("bash", script, "--help").CombinedOutput()
		if err != nil {
			t.Fatalf("resend --help: %v\n%s", err, output)
		}
		if !strings.Contains(string(output), "codefly-dev/provider-resend") {
			t.Fatalf("resend --help did not point at the plugin:\n%s", output)
		}
	})
}

func newSetupWorkspace(t *testing.T) string {
	t.Helper()
	workspace := t.TempDir()
	local := filepath.Join(workspace, "configurations", "local")
	dogfood := filepath.Join(workspace, "configurations", "local-dogfood")
	mustMkdir(t, local)
	mustMkdir(t, dogfood)
	defaults := map[string]string{
		"billing.env":           "BILLING_PROVIDER=disabled\n",
		"email.env":             "EMAIL_PROVIDER=log\nEMAIL_FROM=no-reply@localhost\n",
		"product-analytics.env": "PRODUCT_ANALYTICS_MODE=disabled\nNEXT_PUBLIC_PRODUCT_ANALYTICS_MODE=disabled\n",
		"error-tracking.env":    "ERROR_TRACKING_MODE=disabled\nNEXT_PUBLIC_ERROR_TRACKING_MODE=disabled\nNEXT_PUBLIC_SENTRY_DSN=\n",
		"observability.env":     "OBSERVABILITY_EXPORTER=debug\nOTEL_EXPORTER_OTLP_ENDPOINT=\n",
		"abuse-protection.env":  "ABUSE_PROTECTION_MODE=disabled\nNEXT_PUBLIC_ABUSE_PROTECTION_MODE=disabled\nNEXT_PUBLIC_TURNSTILE_SITE_KEY=\n",
	}
	for name, contents := range defaults {
		mustWrite(t, filepath.Join(local, name), contents, 0o600)
	}
	return workspace
}

func setupScript(t *testing.T, provider string) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate setup script tests")
	}
	return filepath.Join(filepath.Dir(file), "scripts", "setup", provider+".sh")
}

func secretFile(t *testing.T, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "secret.env")
	mustWrite(t, path, contents+"\n", 0o600)
	return path
}

func mustMkdir(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
}

func mustWrite(t *testing.T, path, contents string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), mode); err != nil {
		t.Fatal(err)
	}
}

func assertPrivateFile(t *testing.T, path string) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("%s permissions = %o, want 600", path, info.Mode().Perm())
	}
}
