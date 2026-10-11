package infra

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"accounts/pkg/business"
)

// Who may HAND a delivered document to this host.
//
// Separate from who signed it, and keeping the two apart is the point. The
// bundle says who produced the content; this says who delivered it. A valid
// bundle does not make every caller a legitimate carrier — otherwise anyone who
// could replay a captured carrier could re-deliver it, including a superseded
// generation whose signature is still perfectly good.
//
// The two halves are authorised differently, and the asymmetry is deliberate:
//
//   - AUTHORITY has one fixed (service account, namespace) pair, because there
//     is one authority writer for the whole platform.
//   - PRESENCE is checked against the namespace THE DOCUMENT'S OWN WORKLOADS
//     DECLARE, because a presence Job runs one per module tree in that module's
//     own namespace. A fixed value would refuse every genuine carrier.
//
// A generation that declares no workload declares no namespace, and a TOMBSTONE
// is exactly that: removal is a generation, and it carries nothing. Refusing the
// empty case outright therefore made removal through this endpoint IMPOSSIBLE.
// The caller resolves such a document against the namespace this host last
// applied for that binding, which is its own recorded state rather than
// anything the arriving document asserts.
//
// Reading the presence namespace out of the signed document rather than from
// configuration is what keeps it out of a deployer's reach — the same property
// that puts the ownership domain inside the canonical bytes. And this host's
// check is the SECOND layer, not the perimeter: a manifest claiming a namespace
// other than the one its delivery tree declares is refused by the delivery
// controller's own destination list, at apply.

// The delivery service account, for both kinds. One name rather than two: the
// renderer uses a single constant, and a second name here would be a constant
// only this host believes in — which fails with a TokenReview that SUCCEEDS on a
// genuine identity, refused for a name.
const deliveryServiceAccount = "delivery"

// The authority half's fixed namespace.
const authorityDeliveryNamespace = "platform-authority"

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
// writer for this kind of document. Terminal.
//
// Distinct from unauthenticated because the two are fixed in different places: a
// refused token is the caller's credential, and a wrong service account or
// namespace is the deployment's wiring.
var ErrCarrierNotAuthorized = errors.New("delivery carrier is not the authorized writer for this document kind")

// SolutionDeliveryCarrierCheck authorises a carrier by TokenReview.
type SolutionDeliveryCarrierCheck struct {
	kubernetes *KubernetesClient
}

// NewSolutionDeliveryCarrierCheck builds the carrier authorizer.
func NewSolutionDeliveryCarrierCheck(kubernetes *KubernetesClient) *SolutionDeliveryCarrierCheck {
	return &SolutionDeliveryCarrierCheck{kubernetes: kubernetes}
}

// AuthorizeCarrier reviews the caller's token and checks the identity it
// authenticates as against the writer this document kind requires.
//
// The failure taxonomy is the contract, because each class has a different right
// response from the caller:
//
//   - no token, or a token the API server REFUSED → ErrCarrierUnauthenticated,
//     terminal (401). The credential is wrong; retrying asks the same question.
//   - authenticated, wrong writer → ErrCarrierNotAuthorized, terminal (403).
//   - the API server could not be ASKED → ErrKubernetesUnavailable, retryable
//     (503). Nothing was verified, so nothing may be concluded.
//
// The third is why the Kubernetes client separates "refused" from "unreachable"
// at all: a control-plane outage reported as a refusal would look like a fleet
// of misconfigured callers, and an operator would go and check the callers.
func (c *SolutionDeliveryCarrierCheck) AuthorizeCarrier(
	ctx context.Context, credential string, kind business.SolutionDeliveryKind, declaredNamespaces []string,
) error {
	if c == nil || c.kubernetes == nil {
		// Fail closed. A deployment that mounted the endpoint without a way to
		// review carriers must not accept documents from anyone.
		return fmt.Errorf("%w: this host cannot review a carrier's credential", ErrKubernetesUnavailable)
	}
	token := bearerToken(credential)
	if token == "" {
		return fmt.Errorf("%w: no bearer token was presented", ErrCarrierUnauthenticated)
	}

	reviewed, err := c.kubernetes.ReviewToken(ctx, token, deliveryTokenAudience)
	if err != nil {
		if errors.Is(err, ErrTokenRejected) {
			return fmt.Errorf("%w: %w", ErrCarrierUnauthenticated, err)
		}
		// Unreachable, or the host's own RBAC refused: nothing was verified.
		return err
	}

	if reviewed.ServiceAccount != deliveryServiceAccount {
		return fmt.Errorf("%w: %s is delivered by service account %q, and the carrier authenticated as %q",
			ErrCarrierNotAuthorized, kind, deliveryServiceAccount, reviewed.ServiceAccount)
	}

	switch kind {
	case business.SolutionDeliveryAuthority:
		if reviewed.Namespace != authorityDeliveryNamespace {
			return fmt.Errorf("%w: authority is delivered from namespace %q, and the carrier authenticated in %q",
				ErrCarrierNotAuthorized, authorityDeliveryNamespace, reviewed.Namespace)
		}
		return nil

	case business.SolutionDeliveryPresence:
		// The namespace comes from the signed document's own workloads, or —
		// for a generation that declares none — from the namespace this host
		// last APPLIED for that binding. Which of the two answered is the
		// caller's to resolve; see business.presenceCarrierNamespaces.
		//
		// Empty means neither did: a first generation for a binding nothing has
		// applied, declaring no workload. That cannot be authorised at all —
		// there is nothing to check the carrier against, and accepting it would
		// make the namespace check vacuous for exactly the documents that
		// omitted one.
		if len(declaredNamespaces) == 0 {
			return fmt.Errorf("%w: neither this presence document nor the binding's applied generation names a workload namespace, so there is nothing to authorise its carrier against",
				ErrCarrierNotAuthorized)
		}
		// More than one distinct namespace has no single answer to "where was
		// this delivered from". The renderer uses one namespace per render and
		// cannot emit such a document, so one that exists was hand-built —
		// refused by name rather than resolved by picking one.
		if len(declaredNamespaces) > 1 {
			return fmt.Errorf("%w: the presence document declares workloads in %d namespaces (%s), so the namespace it was delivered from has no single answer",
				ErrCarrierNotAuthorized, len(declaredNamespaces), strings.Join(declaredNamespaces, ", "))
		}
		if reviewed.Namespace != declaredNamespaces[0] {
			return fmt.Errorf("%w: the presence document's workloads declare namespace %q, and the carrier authenticated in %q",
				ErrCarrierNotAuthorized, declaredNamespaces[0], reviewed.Namespace)
		}
		return nil
	}
	return fmt.Errorf("%w: unknown document kind %q", ErrCarrierNotAuthorized, kind)
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
