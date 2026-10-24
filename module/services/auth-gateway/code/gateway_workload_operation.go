package main

// The BEARER-LESS ARM of a declared module route: a host-minted headless
// operation context presented at /v1/<alias>/* on the workload endpoint.
//
// A composed workload that mints an operation context at
// /modules/_operation-context holds a capability bound to the target module's
// own audience, and no person's bearer: there is no person. The person path
// (ext_authz, then per-viewer installation admission) cannot admit it, and
// before this arm such a context could reach its callee only by dialling it
// in-mesh. The arm admits it at the one door instead, without widening what a
// bearer-less request can reach anywhere else.
//
// Selection is narrow and every other request takes the person path unchanged:
// no Authorization and no Cookie header, the workload listener, and an
// edge-inspected capability addressed to the alias this route resolved. Once
// selected, the arm decides from three signed facts and one authority read:
//
//   - the tenant and the seal the capability names (core's Inspector checked the
//     signature, issuer, window, audience and shape at the route);
//   - the tenant's ACTIVE installations, read from accounts per request.
//
// It admits only when the tenant has an active installation of the TARGET, and
// the seal names an active installation in that tenant that is none of the
// target's. The second half
// is how a hop that reads no owner tells unattended work from a person: a
// person's capability is sealed to the organization's installation of the
// audience it addresses, which here is the target's own, while a module or
// installation principal's is sealed to its own installation. Accounts holds
// that as an invariant (TestAPersonsCapabilityIsSealedToItsAudiencesInstallation
// in accounts); this arm is sound only while it holds. A misreading refuses,
// never widens, and the callee verifies the capability at full strength.
//
// What the upstream receives: the capability as presented, the path unchanged,
// no bearer, no cookie, no identity header, and x-codefly-installation-id set to
// the seal. Nothing names a principal, because nothing here read one.

import (
	"context"
	"log"
	"net/http"

	accountsv1 "auth-gateway/pkg/gen/saas/accounts/v1"

	"github.com/codefly-dev/core/workcontext"
	carrier "github.com/codefly-dev/sdk-go/workcontext"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

const listInstallationsMethod = "/saas.accounts.v1.InstallationService/ListInstallations"

// installationListingPageSize and installationListingPageLimit bound one
// admission read the way the entitlement read beside it is bounded: a cursor
// that never terminates is undecidable, never a short answer.
const (
	installationListingPageSize  = 200
	installationListingPageLimit = solutionEntitlementPageLimit
)

// installationListingClient is the authority surface the arm consumes. The
// interface exists so the arm can be exercised without an accounts process.
type installationListingClient interface {
	List(ctx context.Context, req *accountsv1.ListInstallationsRequest) (*accountsv1.ListInstallationsResponse, error)
}

// accountsInstallations invokes the organization's installation listing by
// method name over the existing accounts connection, with the gateway's
// cluster-internal credential: the shape of accountsSolutionEntitlements, and for
// the same reason (only the vendored message types, no client stub).
type accountsInstallations struct {
	conn          *grpc.ClientConn
	internalToken string
}

func (c *accountsInstallations) List(
	ctx context.Context, req *accountsv1.ListInstallationsRequest,
) (*accountsv1.ListInstallationsResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, solutionEntitlementCallTimeout)
	defer cancel()
	ctx = metadata.AppendToOutgoingContext(ctx, "x-codefly-internal-token", c.internalToken)
	resp := &accountsv1.ListInstallationsResponse{}
	return resp, c.conn.Invoke(ctx, listInstallationsMethod, req, resp)
}

// workloadOperationTenantKey carries the signed tenant from the arm to the rate
// limiter. A private context key, set only by serveWorkloadOperation: no header
// and no other route can select the budget it keys.
type workloadOperationTenantKey struct{}

// workloadOperationTenant reports the tenant the arm admitted the request under,
// for the limiter's namespaced budget.
func workloadOperationTenant(ctx context.Context) (string, bool) {
	tenant, ok := ctx.Value(workloadOperationTenantKey{}).(string)
	return tenant, ok && tenant != ""
}

// selectsWorkloadOperation is conditions 1 to 5: whether the request takes the
// bearer-less arm at all. Any one failing leaves the person path to answer
// exactly as it did before the arm existed.
func selectsWorkloadOperation(r *http.Request, capability *workcontext.Inspected, alias string) bool {
	return len(r.Header.Values("Authorization")) == 0 &&
		len(r.Header.Values("Cookie")) == 0 &&
		onWorkloadListener(r.Context()) &&
		capability != nil &&
		alias != moduleCapabilitiesAudience &&
		capability.Audience() == alias
}

// workloadOperationAdmission is the arm's authority verdict. Like the viewer
// admission it is not a boolean: "not installed", "not unattended work" and "I
// could not ask" are different answers.
type workloadOperationAdmission int

const (
	workloadOperationAdmitted workloadOperationAdmission = iota
	// workloadOperationNotInstalled: the tenant has no ACTIVE installation of
	// the target, or the route resolved no target.
	workloadOperationNotInstalled
	// workloadOperationPersonSealed: the seal is one of the target's own
	// installations, which is what a person's capability for this module
	// carries.
	workloadOperationPersonSealed
	// workloadOperationSealInactive: the seal names no ACTIVE installation in
	// the tenant (revoked, another tenant's, unknown).
	workloadOperationSealInactive
	// workloadOperationUndecidable: no client wired, an RPC error, or a cursor
	// that did not terminate. Answered 503, never admitted.
	workloadOperationUndecidable
)

// admitWorkloadOperation reads the tenant's ACTIVE installations and decides.
// Uncached, for the reason the viewer admission is: an uninstall changes the
// answer with no write this replica would see.
func (g *Gateway) admitWorkloadOperation(
	ctx context.Context, tenant, seal string, routing *solutionRouting,
) workloadOperationAdmission {
	targetID := routing.GetTargetID()
	if targetID == "" {
		return workloadOperationNotInstalled
	}
	if g.installations == nil {
		return workloadOperationUndecidable
	}
	// Every ACTIVE row of the target counts: the seal of a person's capability
	// for this module is one of them, and keeping only the last row seen would
	// read the others as some other installation's.
	targetInstallations := map[string]bool{}
	sealActive := false
	token := ""
	for page := 0; page < installationListingPageLimit; page++ {
		resp, err := g.installations.List(ctx, &accountsv1.ListInstallationsRequest{
			OrgId:     tenant,
			Status:    accountsv1.InstallationStatus_INSTALLATION_STATUS_ACTIVE,
			PageSize:  installationListingPageSize,
			PageToken: token,
		})
		if err != nil {
			return workloadOperationUndecidable
		}
		for _, summary := range resp.GetInstallations() {
			installation := summary.GetInstallation()
			// The request asked for ACTIVE rows of this tenant; a row that is
			// neither is not counted, whatever the authority returned.
			if installation.GetId() == "" || installation.GetOrgId() != tenant ||
				installation.GetStatus() != accountsv1.InstallationStatus_INSTALLATION_STATUS_ACTIVE {
				continue
			}
			if installation.GetTargetId() == targetID {
				targetInstallations[installation.GetId()] = true
			}
			if installation.GetId() == seal {
				sealActive = true
			}
		}
		// No early exit: whether the seal is ANOTHER row of the target is only
		// conclusive after the whole set, so every page is read.
		token = resp.GetNextPageToken()
		if token == "" {
			break
		}
		if page == installationListingPageLimit-1 {
			return workloadOperationUndecidable
		}
	}
	switch {
	case len(targetInstallations) == 0:
		return workloadOperationNotInstalled
	case targetInstallations[seal]:
		return workloadOperationPersonSealed
	case !sealActive:
		return workloadOperationSealInactive
	}
	return workloadOperationAdmitted
}

// workloadOperationRecord is what every admit and refusal on the arm logs. The
// arm runs no ext_authz, so this line is its only decision record. It names the
// signed facts and the presenter, never the token and never an authority error.
type workloadOperationRecord struct {
	method, path, alias, tenant, task, seal, presenter string
}

func (rec workloadOperationRecord) log(verdict, reason string) {
	presenter := rec.presenter
	if presenter == "" {
		presenter = "local"
	}
	log.Printf("%s request: method=%s path=%q reason=workload_operation_%s alias=%q tenant=%q task=%q seal=%q presenter=%q",
		verdict, rec.method, rec.path, reason, rec.alias, rec.tenant, rec.task, rec.seal, presenter)
}

func (rec workloadOperationRecord) refuse(w http.ResponseWriter, code int, body, reason string) {
	rec.log("WARN: blocked", reason)
	httpError(w, code, body)
}

// serveWorkloadOperation is conditions 6 to 12, for a request
// selectsWorkloadOperation chose.
func (g *Gateway) serveWorkloadOperation(
	w http.ResponseWriter, r *http.Request, alias string, routing *solutionRouting, capability *workcontext.Inspected,
) {
	rec := workloadOperationRecord{
		method: r.Method, path: r.URL.Path, alias: alias,
		tenant: capability.TenantID(), task: capability.TaskID(), seal: capability.InstallationID(),
		presenter: workloadPresenter(r.Context()),
	}
	// On a cell the workload listener has already required the waypoint's
	// presenter. Required again here, so a listener wired without it cannot
	// admit a capability that nothing in the mesh vouched for.
	if g.requireWorkloadPresenter && rec.presenter == "" {
		rec.refuse(w, http.StatusUnauthorized, "authentication required", "presenter_missing")
		return
	}
	// Exactly one capability. Core's Inspector read the first value the
	// request carried; a second one is a value nothing here verified, and an
	// upstream that read it instead would act on it.
	if len(r.Header.Values(carrier.HeaderName)) != 1 {
		rec.refuse(w, http.StatusUnauthorized, "invalid work context", "capability_not_single")
		return
	}
	if rec.tenant == "" {
		rec.refuse(w, http.StatusUnauthorized, "invalid work context", "no_tenant")
		return
	}
	if rec.seal == "" {
		rec.refuse(w, http.StatusUnauthorized, "invalid work context", "no_seal")
		return
	}

	// Strip BEFORE the limiter, as the person path does: a caller's X-Org-Id
	// must never reach the key, and nothing below may read a header the caller
	// wrote. No bearer and no cookie arrived (selection), and none leaves.
	stripAllIdentityHeaders(r)
	r.Header.Del("Authorization")
	r.Header.Del("Proxy-Authorization")
	// A client certificate chain is the mesh's statement about the hop that
	// reached this gateway, not about the caller, and nothing upstream may read
	// one off a request this arm forwards. The authenticated listener already
	// removed it on a cell; natively nothing did.
	r.Header.Del("X-Forwarded-Client-Cert")

	// The budget is the signed tenant's own namespace, fail closed: with no
	// X-Org-Id the generic key would be the waypoint's address, one bucket for
	// every tenant on the cell. The admission read sits behind it.
	entry := &RouteEntry{
		Service:                    moduleServicePrefix + alias,
		Protected:                  true,
		RateLimitBackendFailClosed: true,
		staticUpstreamError:        true,
	}
	r = r.WithContext(context.WithValue(r.Context(), workloadOperationTenantKey{}, rec.tenant))
	reached := false
	g.rateLimitThenServe(w, r, entry, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
		switch g.admitWorkloadOperation(r.Context(), rec.tenant, rec.seal, routing) {
		case workloadOperationUndecidable:
			rec.refuse(w, http.StatusServiceUnavailable, "module installation authority unavailable", "authority_unavailable")
			return
		case workloadOperationNotInstalled:
			rec.refuse(w, http.StatusForbidden, "module is not installed for this organization", "not_installed")
			return
		case workloadOperationPersonSealed:
			// The answer a person's capability without its bearer has always had.
			rec.refuse(w, http.StatusUnauthorized, "authentication required", "person_sealed")
			return
		case workloadOperationSealInactive:
			rec.refuse(w, http.StatusUnauthorized, "authentication required", "seal_inactive")
			return
		}
		// The one header this arm stamps: the seal of the capability it admitted.
		// An sdk consumer reading the carrier requires this header to equal the
		// seal, and on the person path it equals the seal too. Here it names the
		// CALLER's installation, not consent on the target's side.
		r.Header.Set(installationConfinementHeader, rec.seal)
		rec.log("INFO: admitted", "admit")
		g.proxyTo(w, r, routing.Upstream, entry)
	}))
	if !reached {
		// Refused by the limiter (429, or 503 with its backend down) or by the
		// cross-origin binding, each of which wrote its own answer; the log
		// cannot tell which, and the reason says both.
		rec.log("WARN: blocked", "limited_or_origin")
	}
}
