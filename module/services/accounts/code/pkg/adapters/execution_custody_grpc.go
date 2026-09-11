package adapters

import (
	"accounts/pkg/auth"
	gen "accounts/pkg/gen/saas/accounts/v1"
	"crypto/tls"
	"errors"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

// NewCustodyWorkContextGRPC composes the canonical WorkContext service with the
// existing Accounts method-policy interceptor. Separate listeners are mandatory:
// internal=true accepts only internal revision/evidence RPCs and their separate
// SetInternalToken credential; tenant calls require the real owner JWTMinter.
// The owning host installs WithService before serving, exactly as Accounts does.
func NewCustodyWorkContextGRPC(authority *WorkContextAuthorityServer, minter auth.JWTMinter, internal bool, tc *tls.Config) (*grpc.Server, error) {
	if authority == nil || authority.configureErr != nil || authority.verifier == nil || minter == nil || tc == nil || len(tc.Certificates) == 0 {
		return nil, errors.New("configured Accounts authority and TLS required")
	}
	config := tc.Clone()
	config.MinVersion = tls.VersionTLS13
	config.GetConfigForClient = nil
	exposure := rpcExposureTenant
	if internal {
		exposure = rpcExposureInternal
	}
	server := grpc.NewServer(grpc.Creds(credentials.NewTLS(config)), grpc.ChainUnaryInterceptor(grpcAuthInterceptor(func() auth.JWTMinter { return minter }, exposure)), grpc.MaxRecvMsgSize(128<<10))
	gen.RegisterWorkContextServiceServer(server, authority)
	return server, nil
}
