package adapters

// What authority 0.3.x adds to the interface: the hop's admission keys
// (0.3.2, 0.3.3), served; and installation-owned resources (0.3.0), which
// this host does not record yet, so each of those methods and an
// owner-selected ask is refused by name rather than read as something wider.

import (
	"context"
	"errors"

	"github.com/codefly-dev/core/workcontext"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"accounts/pkg/business"
	authorityv1 "accounts/pkg/gen/host/authority/v1"
)

// The purpose each published key carries: a verifier uses a key only for its
// own purpose. admissionKeyPurpose is stamp.Purpose in the authority client.
const (
	workContextKeyPurpose = "work-context"
	admissionKeyPurpose   = "hop-admission"
)

// admissionKeys registers and publishes the hop's admission keys.
type admissionKeys interface {
	RegisterAdmissionKey(ctx context.Context, credential string, public []byte) (business.AdmissionKey, error)
	PublishedAdmissionKeys(ctx context.Context) ([]business.AdmissionKey, error)
}

func (s *HostAuthorityServer) admissionKeys() admissionKeys {
	if s.admissions != nil {
		return s.admissions
	}
	if service == nil {
		return nil
	}
	return service
}

// RegisterAdmissionKey publishes the hop's admission key in Keys until its
// registration lapses, for the hop alone: the caller proves it is the hop by
// its workload credential, and any other workload is refused by name.
func (s *HostAuthorityServer) RegisterAdmissionKey(ctx context.Context, req *authorityv1.RegisterAdmissionKeyRequest) (*authorityv1.RegisterAdmissionKeyResponse, error) {
	keys := s.admissionKeys()
	if keys == nil {
		return nil, status.Error(codes.Unavailable, business.ErrAdmissionKeysUnwired.Error())
	}
	key, err := keys.RegisterAdmissionKey(ctx, req.GetActor().GetCredential(), req.GetPublicKey())
	switch {
	case err == nil:
	case errors.Is(err, business.ErrAdmissionKeyNotTheHop):
		return nil, status.Error(codes.PermissionDenied, err.Error())
	case errors.Is(err, business.ErrAdmissionKeyMalformed):
		return nil, status.Error(codes.InvalidArgument, err.Error())
	case errors.Is(err, business.ErrAdmissionKeysFull):
		return nil, status.Error(codes.FailedPrecondition, err.Error())
	case errors.Is(err, business.ErrAdmissionKeysUnwired):
		return nil, status.Error(codes.Unavailable, err.Error())
	case errors.Is(err, business.ErrExecutionUnbound), errors.Is(err, business.ErrExecutionNotBoundToken),
		errors.Is(err, business.ErrExecutionTokenRejected), errors.Is(err, business.ErrModuleIdentityDenied):
		return nil, credentialRefusal(err, "actor")
	default:
		return nil, status.Errorf(codes.Unavailable, "the admission key could not be registered: %v", err)
	}
	return &authorityv1.RegisterAdmissionKeyResponse{Key: admissionKeyToWire(key), ExpiresAtUnix: key.ExpiresAt.Unix()}, nil
}

// publishedAdmissionKeys is Keys' admission_keys. A store that cannot be read
// fails Keys rather than publishing none: a verifier keeps the keys it holds
// through a failed read, but would refuse every admission after an empty one.
func (s *HostAuthorityServer) publishedAdmissionKeys(ctx context.Context) ([]*authorityv1.PublicKey, error) {
	keys := s.admissionKeys()
	if keys == nil {
		return nil, status.Error(codes.Unavailable, business.ErrAdmissionKeysUnwired.Error())
	}
	published, err := keys.PublishedAdmissionKeys(ctx)
	if err != nil {
		return nil, status.Errorf(codes.Unavailable, "the hop's admission keys could not be read: %v", err)
	}
	out := make([]*authorityv1.PublicKey, 0, len(published))
	for _, key := range published {
		out = append(out, admissionKeyToWire(key))
	}
	return out, nil
}

func admissionKeyToWire(key business.AdmissionKey) *authorityv1.PublicKey {
	return &authorityv1.PublicKey{
		KeyId: key.ID, Algorithm: workcontext.Algorithm, PublicKey: []byte(key.PublicKey), Purpose: admissionKeyPurpose,
	}
}

// installationOwnedGap names what this host does not do yet.
const installationOwnedGap = "this host does not record installation-owned resources yet (codefly.dev/authority@0.3.0)"

// AttachResource is refused by name: no installation owns a resource here.
func (s *HostAuthorityServer) AttachResource(context.Context, *authorityv1.AttachResourceRequest) (*authorityv1.AttachResourceResponse, error) {
	return nil, status.Error(codes.Unimplemented, "AttachResource: "+installationOwnedGap)
}

// DetachResource is refused by name: no installation owns a resource here.
func (s *HostAuthorityServer) DetachResource(context.Context, *authorityv1.DetachResourceRequest) (*authorityv1.DetachResourceResponse, error) {
	return nil, status.Error(codes.Unimplemented, "DetachResource: "+installationOwnedGap)
}

// AuthorizeTransfer is refused by name: no installation owns a resource here.
func (s *HostAuthorityServer) AuthorizeTransfer(context.Context, *authorityv1.AuthorizeTransferRequest) (*authorityv1.AuthorizeTransferResponse, error) {
	return nil, status.Error(codes.Unimplemented, "AuthorizeTransfer: "+installationOwnedGap)
}

// refuseOwnerSelected refuses an ask naming a resource owner. This host's
// scopes carry no owner, so honouring the rest of the scope would issue the
// whole kind where the caller asked for one owner's resources.
func refuseOwnerSelected(scopes []*authorityv1.WorkScopeV1) error {
	for _, scope := range scopes {
		if scope.GetResourceOwnerId() != "" {
			return status.Errorf(codes.Unimplemented, "a scope on %s selects owner %q: %s", scope.GetResourceKind(), scope.GetResourceOwnerId(), installationOwnedGap)
		}
	}
	return nil
}
