package auth_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"accounts/pkg/auth"
)

func TestParseSignupMode(t *testing.T) {
	cases := []struct {
		raw  string
		want auth.SignupMode
	}{
		{"", auth.SignupModeOpen},
		{"open", auth.SignupModeOpen},
		{"OPEN", auth.SignupModeOpen},
		{"  open  ", auth.SignupModeOpen},
		{"invite", auth.SignupModeInvite},
		{"Invite", auth.SignupModeInvite},
		{"waitlist", auth.SignupModeWaitlist},
		{"  WaitList ", auth.SignupModeWaitlist},
	}
	for _, tc := range cases {
		mode, err := auth.ParseSignupMode(tc.raw)
		require.NoErrorf(t, err, "ParseSignupMode(%q)", tc.raw)
		require.Equalf(t, tc.want, mode, "ParseSignupMode(%q)", tc.raw)
	}
}

func TestParseSignupMode_UnrecognisedFailsClosed(t *testing.T) {
	for _, raw := range []string{"closed", "public", "on", "true", "yes", "openn"} {
		mode, err := auth.ParseSignupMode(raw)
		require.Errorf(t, err, "ParseSignupMode(%q) must reject unrecognised values", raw)
		require.NotEqualf(t, auth.SignupModeOpen, mode,
			"ParseSignupMode(%q) must not fall back to open when the value is unrecognised", raw)
	}
}
