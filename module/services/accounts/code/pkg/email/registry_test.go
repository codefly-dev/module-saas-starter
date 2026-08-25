package email

import (
	"context"
	"errors"
	"testing"
)

func TestRegistrySelectsRegisteredProvider(t *testing.T) {
	registry := NewRegistry()
	fake := NewFakeSender()
	registry.Register("Fake", func(context.Context) (Sender, error) { return fake, nil })

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
	registry.Register("resend", func(context.Context) (Sender, error) { return nil, nil })
	registry.Register("log", func(context.Context) (Sender, error) { return nil, nil })

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
	registry.Register("resend", func(context.Context) (Sender, error) { return nil, sentinel })

	_, err := registry.Select(context.Background(), "resend")
	if !errors.Is(err, sentinel) {
		t.Fatalf("Select error = %v, want the factory's error", err)
	}
}
