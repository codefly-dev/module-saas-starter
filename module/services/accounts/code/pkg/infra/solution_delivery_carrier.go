package infra

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"accounts/pkg/business"
)

// Who may HAND a delivered manifest to this host.
//
// Separate from who signed it, and keeping the two apart is the point. The
// envelope says who produced the content; this says who delivered it. A valid
// signature does not make every caller a legitimate carrier — otherwise anyone
// who could replay a captured carrier could re-deliver it, including a
// superseded generation whose signature is still perfectly good.
//
// The carrier is the `delivery` ServiceAccount of the module's own namespace:
// one delivery Job per module tree runs there. The namespace is the module's by
// the cell's rule (business.ModuleNamespaceRule) — the manifest's module, or for
// a withdrawal the module of the binding's previously applied manifest — so it
// comes from signed bytes and this host's own placement, never from
// configuration a deployer can edit.

// deliveryServiceAccount is the account a delivery carrier runs as: the CLI's
// constant (the `delivery` ServiceAccount its render creates per module
// namespace), also core's workloadidentity.DeliveryService.
const deliveryServiceAccount = "delivery"

// The audience a delivery carrier's token must be minted for. The same audience
// the credential mint uses, because accounts is the service performing the
// review in both cases and a second audience is another projected token to
// mis-mount.
const deliveryTokenAudience = "accounts"

// ErrCarrierUnauthenticated reports that the carrier's credential was reviewed
// and refused, or was absent. Terminal: the token is kubelet-projected and
// re-read per request, so a refusal is a configuration fault and a retry asks
// the same question.
var ErrCarrierUnauthenticated = errors.New("delivery carrier is not authenticated")

// ErrCarrierNotAuthorized reports that the carrier authenticated but is not the
// delivery account of the manifest's module namespace. Terminal.
//
// Distinct from unauthenticated because the two are fixed in different places: a
// refused token is the caller's credential, and a wrong service account or
// namespace is the deployment's wiring.
var ErrCarrierNotAuthorized = errors.New("delivery carrier is not the delivery account of the manifest's module namespace")

// SolutionDeliveryCarrierCheck authorises a carrier by TokenReview.
type SolutionDeliveryCarrierCheck struct {
	kubernetes *KubernetesClient
}

// AuthorizeAppliedObserver checks the separate protected publisher identity.
// The delivery writer cannot use this read, and the publisher cannot use the
// document delivery write. Both reuse the accounts-audience TokenReview seam.
func (c *SolutionDeliveryCarrierCheck) AuthorizeAppliedObserver(ctx context.Context, credential string) error {
	if c == nil || c.kubernetes == nil {
		return fmt.Errorf("%w: this host cannot review an observer's credential", ErrKubernetesUnavailable)
	}
	token := bearerToken(credential)
	if token == "" {
		return fmt.Errorf("%w: no bearer token was presented", ErrCarrierUnauthenticated)
	}
	reviewed, err := c.kubernetes.reviewToken(ctx, token, deliveryTokenAudience)
	if err != nil {
		if errors.Is(err, ErrTokenRejected) {
			return fmt.Errorf("%w: %w", ErrCarrierUnauthenticated, err)
		}
		return err
	}
	if reviewed.Namespace != "istio-presenter" || reviewed.ServiceAccount != "endpoint-publisher" {
		return fmt.Errorf("%w: caller is not the protected applied-state observer", ErrCarrierNotAuthorized)
	}
	return nil
}

// NewSolutionDeliveryCarrierCheck builds the carrier authorizer.
func NewSolutionDeliveryCarrierCheck(kubernetes *KubernetesClient) *SolutionDeliveryCarrierCheck {
	return &SolutionDeliveryCarrierCheck{kubernetes: kubernetes}
}

// AuthorizeCarrier reviews the caller's token and requires it to authenticate
// as the delivery account of namespace.
//
// The failure taxonomy is the contract, because each class has a different right
// response from the caller:
//
//   - no token, or a token the API server REFUSED → ErrCarrierUnauthenticated,
//     terminal (401). The credential is wrong; retrying asks the same question.
//   - authenticated, wrong account or namespace → ErrCarrierNotAuthorized,
//     terminal (403).
//   - the API server could not be ASKED → ErrKubernetesUnavailable, retryable
//     (503). Nothing was verified, so nothing may be concluded.
func (c *SolutionDeliveryCarrierCheck) AuthorizeCarrier(ctx context.Context, credential, namespace string) error {
	if c == nil || c.kubernetes == nil {
		// Fail closed. A deployment that mounted the endpoint without a way to
		// review carriers must not accept documents from anyone.
		return fmt.Errorf("%w: this host cannot review a carrier's credential", ErrKubernetesUnavailable)
	}
	token := bearerToken(credential)
	if token == "" {
		return fmt.Errorf("%w: no bearer token was presented", ErrCarrierUnauthenticated)
	}
	reviewed, err := c.kubernetes.reviewToken(ctx, token, deliveryTokenAudience)
	if err != nil {
		if errors.Is(err, ErrTokenRejected) {
			return fmt.Errorf("%w: %w", ErrCarrierUnauthenticated, err)
		}
		// Unreachable, or the host's own RBAC refused: nothing was verified.
		return err
	}
	if reviewed.ServiceAccount != deliveryServiceAccount || reviewed.Namespace != namespace {
		return fmt.Errorf("%w: the manifest is delivered by %s/%s, and the carrier authenticated as %s/%s",
			ErrCarrierNotAuthorized, namespace, deliveryServiceAccount, reviewed.Namespace, reviewed.ServiceAccount)
	}
	return nil
}

// bearerToken reads the token out of an Authorization header value.
//
// The scheme is matched case-insensitively because HTTP says it is
// case-insensitive, and a caller sending `bearer` would otherwise be refused as
// unauthenticated for a reason no error message would make obvious.
func bearerToken(credential string) string {
	fields := strings.Fields(strings.TrimSpace(credential))
	if len(fields) != 2 || !strings.EqualFold(fields[0], "bearer") {
		return ""
	}
	return fields[1]
}

var _ business.SolutionDeliveryCarrierAuthorizer = (*SolutionDeliveryCarrierCheck)(nil)
var _ business.SolutionDeliveryObserverAuthorizer = (*SolutionDeliveryCarrierCheck)(nil)
