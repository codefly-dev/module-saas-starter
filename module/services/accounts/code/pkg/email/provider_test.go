package email

import (
	"context"
	"errors"
	"testing"
)

// Every shipped provider must satisfy the Provider interface, not just Sender.
var (
	_ Provider = (*ResendSender)(nil)
	_ Provider = (*LogSender)(nil)
	_ Provider = (*FakeSender)(nil)
)

func TestProviderCapabilityMatrix(t *testing.T) {
	resend, err := NewResendSender(ResendConfig{APIKey: "re_test"})
	if err != nil {
		t.Fatalf("NewResendSender: %v", err)
	}

	cases := []struct {
		name     string
		provider Provider
		want     Capabilities
	}{
		{
			name:     "resend",
			provider: resend,
			want: Capabilities{
				IdempotencyKeys:        true,
				DeliveryWebhooks:       true,
				BatchSend:              true,
				VerifiedSenderRequired: true,
				Tagging:                true,
			},
		},
		{
			name:     "log",
			provider: NewLogSender(func(string, ...any) {}),
			want:     Capabilities{},
		},
		{
			name:     "fake",
			provider: NewFakeSender(),
			want:     Capabilities{},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.provider.Name() != tc.name {
				t.Errorf("Name() = %q, want %q", tc.provider.Name(), tc.name)
			}
			if got := tc.provider.Capabilities(); got != tc.want {
				t.Errorf("Capabilities() = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestFakeSenderCapabilitiesAreConfigurable(t *testing.T) {
	fake := NewFakeSender()
	fake.Caps = Capabilities{DeliveryWebhooks: true}
	if !fake.Capabilities().DeliveryWebhooks {
		t.Fatal("FakeSender.Capabilities() must reflect Caps")
	}
}

func TestRegistrySelectsRegisteredProvider(t *testing.T) {
	registry := NewRegistry()
	fake := NewFakeSender()
	registry.Register("Fake", func(context.Context) (Provider, error) { return fake, nil })

	got, err := registry.Select(context.Background(), " fake ")
	if err != nil {
		t.Fatalf("Select: %v", err)
	}
	if got != fake {
		t.Fatal("Select returned a different provider than was registered")
	}
}

func TestRegistryUnknownProviderFailsClosed(t *testing.T) {
	registry := NewRegistry()
	registry.Register("resend", func(context.Context) (Provider, error) { return nil, nil })
	registry.Register("log", func(context.Context) (Provider, error) { return nil, nil })

	_, err := registry.Select(context.Background(), "gmail")
	if err == nil {
		t.Fatal("expected an error for an unregistered provider")
	}
	if got := err.Error(); got != "EMAIL_PROVIDER must be one of: log, resend" {
		t.Errorf("error = %q, want the sorted registered names", got)
	}
}

func TestRegistryPropagatesFactoryError(t *testing.T) {
	registry := NewRegistry()
	sentinel := errors.New("missing secret")
	registry.Register("resend", func(context.Context) (Provider, error) { return nil, sentinel })

	_, err := registry.Select(context.Background(), "resend")
	if !errors.Is(err, sentinel) {
		t.Fatalf("Select error = %v, want the factory's error", err)
	}
}
