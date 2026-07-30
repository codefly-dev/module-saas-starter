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
	for _, provider := range []string{
		"stripe", "resend", "posthog", "sentry", "otel", "turnstile",
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
	mustWrite(t, codefly, "#!/usr/bin/env bash\nif [[ \"$1\" == endpoint ]]; then printf 'http://localhost:42152\\n'; exit 0; fi\nexit 0\n", 0o755)
	path := bin + string(os.PathListSeparator) + os.Getenv("PATH")

	cases := []struct {
		name    string
		args    []string
		secrets []string
		public  string
		secret  string
	}{
		{
			name: "stripe",
			args: []string{
				"--api-key-file", secretFile(t, "STRIPE_API_KEY=sk_test_codeflyfixture"),
				"--webhook-secret-file", secretFile(t, "STRIPE_WEBHOOK_SECRET=whsec_codeflyfixture"),
				"--skip-remote-validation",
			},
			secrets: []string{"sk_test_codeflyfixture", "whsec_codeflyfixture"},
			public:  "billing.env", secret: "billing.secret.env",
		},
		{
			name: "resend",
			args: []string{
				"--api-key-file", secretFile(t, "RESEND_API_KEY=re_codeflyfixture"),
				"--webhook-secret-file", secretFile(t, "RESEND_WEBHOOK_SECRET=whsec_codeflyfixture"),
				"--from", "Codefly <dogfood@example.com>",
				"--skip-remote-validation",
			},
			secrets: []string{"re_codeflyfixture", "whsec_codeflyfixture"},
			public:  "email.env", secret: "email.secret.env",
		},
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

func TestRemoteWebhookProvisioningRejectsCodeflyLoopbackOrigin(t *testing.T) {
	workspace := newSetupWorkspace(t)
	bin := filepath.Join(t.TempDir(), "bin")
	mustMkdir(t, bin)
	codefly := filepath.Join(bin, "codefly")
	mustWrite(t, codefly, "#!/usr/bin/env bash\nif [[ \"$1\" == endpoint ]]; then printf 'http://localhost:42152\\n'; exit 0; fi\nexit 0\n", 0o755)
	path := bin + string(os.PathListSeparator) + os.Getenv("PATH")

	cases := []struct {
		name string
		args []string
	}{
		{
			name: "stripe",
			args: []string{
				"--api-key-file", secretFile(t, "STRIPE_API_KEY=sk_test_codeflyfixture"),
				"--provision-webhook",
			},
		},
		{
			name: "resend",
			args: []string{
				"--api-key-file", secretFile(t, "RESEND_API_KEY=re_codeflyfixture"),
				"--from", "Codefly <dogfood@example.com>",
				"--provision-webhook",
			},
		},
	}

	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			args := append(test.args, "--workspace", workspace, "--skip-doctor")
			commandArgs := append([]string{setupScript(t, test.name)}, args...)
			command := exec.Command("bash", commandArgs...)
			command.Env = append(os.Environ(), "PATH="+path)
			output, err := command.CombinedOutput()
			if err == nil {
				t.Fatalf("%s unexpectedly provisioned a remote localhost webhook", test.name)
			}
			if !strings.Contains(string(output), "--webhook-origin") {
				t.Fatalf("%s did not explain the public ingress requirement:\n%s", test.name, output)
			}
		})
	}
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
