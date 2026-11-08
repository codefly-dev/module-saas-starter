package business

import (
	"context"
	"crypto/ed25519"
	"errors"
	"testing"

	"github.com/codefly-dev/authority/libraries/authority-core/go/authority"
	"github.com/codefly-dev/authority/libraries/authority-core/go/memstore"
	"github.com/codefly-dev/core/workcontext"
	"github.com/stretchr/testify/require"
)

// unreadableCoreStore is an authority core store every read of which fails.
type unreadableCoreStore struct{ authority.Store }

var errCoreStoreUnreadable = errors.New("the core's store cannot be read")

func (unreadableCoreStore) Read(context.Context, func(authority.Tx) error) error {
	return errCoreStoreUnreadable
}

// A core that cannot say whether an audience is a module page is an error of
// the seal's resolution, never a silent fall through to resolving the audience
// as one of accounts' own modules.
func TestAudienceInstallationReturnsAnUnansweredCore(t *testing.T) {
	public, private, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	core, err := authority.New(unreadableCoreStore{memstore.New()}, authority.Config{
		Issuer: "https://authority.example.com", KeyID: "test", Signer: private,
		Keys: staticKeys{"test": public}, Replay: workcontext.NewMemoryReplayStore(), HostInstallsModules: true,
	})
	require.NoError(t, err)
	s := &Service{}
	s.authorityCoreState().core = core

	_, err = s.WorkContextAudienceInstallation(context.Background(), "org", "example-module")
	require.ErrorIs(t, err, errCoreStoreUnreadable)
	require.ErrorContains(t, err, "example-module")
}
