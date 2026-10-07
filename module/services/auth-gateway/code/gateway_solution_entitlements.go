package main

// The per-viewer solution entitlement surface (issue #949).
//
// The host's solution projections — the navigation menu and the per-client
// surface listing — used to answer the same deployment-wide set to every caller,
// because what a viewer may USE was nowhere in the request. This endpoint is what
// puts it there.
//
// Why it lives in the gateway, and not in the route handler that needs it: the
// frontend's own /api/* routes are served by Next in-process and are never
// proxied through here, so nothing stamps identity on them and a browser can put
// any x-org-id it likes on one. Verified identity exists at exactly one place in
// this system — behind ext_authz, whose OkResponse headers this file reads. A
// projection that derived the organization itself would be trusting a claim it
// had not verified, and a caller could read another tenant's menu by editing it.
//
// So the division is the same one gateway_solutions.go states for proxied
// solution traffic: the gateway performs authentication and identity projection,
// and accounts remains the authorization authority. This handler authenticates,
// projects the tenant and the viewer, and asks accounts — which answers from the
// installation set and the scope grant union in one transaction.

import (
	"context"
	"errors"
	"net/http"
	"time"

	accountsv1 "auth-gateway/pkg/gen/saas/accounts/v1"

	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

// solutionEntitlementsSegment answers what the CALLING viewer may use, from the
// identity ext_authz verifies on the request. It is the one `/solutions/*` segment
// whose answer differs per caller.
const solutionEntitlementsSegment = "_entitlements"

// handleSolutionViewerSegment dispatches the `/solutions/*` segments whose answer
// is per viewer. It lives here rather than in the switch in gateway_solutions.go
// so that file keeps naming nothing about installations or entitlements: the
// boundary test in module/tools reads it to assert that the PROXY decision — which
// is still deployment-wide — consults no tenant state, and a dispatch line there
// would defeat a check that is otherwise exact.
func (g *Gateway) handleSolutionViewerSegment(w http.ResponseWriter, r *http.Request, segment string) bool {
	if segment != solutionEntitlementsSegment {
		return false
	}
	g.handleSolutionEntitlements(w, r)
	return true
}

const listSolutionEntitlementsMethod = "/saas.accounts.v1.SolutionEntitlementService/ListSolutionEntitlements"

// solutionEntitlementRefusalHeader names which of the host's OWN entitlement
// refusals an answer is, so the caller can tell them apart from an ext_authz
// verdict on the user's credential. Status codes alone cannot: a refused internal
// credential and a refused user bearer are both 401, and a caller that read the
// first as the second would tell every signed-in user to re-authenticate over a
// server-side credential fault (and drive a refresh-token rotation on every poll).
//
// It is set by both surfaces that consult the authority — this listing and the
// solution proxy in gateway_solutions.go — because the distinction a caller needs
// is the same on each: a 403 that means "nobody granted your organization this"
// is acted on by installing and granting, and a 403 from ext_authz by signing in.
const solutionEntitlementRefusalHeader = "X-Codefly-Entitlement-Refusal"

const (
	// refusalInternalCredential: the CALLING SERVICE's cluster-internal token was
	// not accepted. A deployment fault, never the user's.
	refusalInternalCredential = "internal-credential"
	// refusalNoOrganization: the user authenticated, but the session names no
	// organization, so there is no org-scoped set to answer.
	refusalNoOrganization = "no-organization"
	// refusalNotEntitled: the authority answered, and this viewer's organization
	// has no installation of the solution the request addressed, or no grant
	// reaching this viewer. Set by the proxy, which refuses one named solution
	// rather than listing a set.
	refusalNotEntitled = "not-entitled"
)

// errSolutionEntitlementsUnbounded is what a cursor that never terminates
// produces. It is an error rather than a truncated answer because a short list is
// indistinguishable from a smaller entitled set, and a consumer would narrow a
// menu on it.
var errSolutionEntitlementsUnbounded = errors.New("solution entitlement listing did not terminate")

// solutionEntitlementCallTimeout bounds one authority round trip. A viewer's menu
// blocks on this, so a stalled accounts must surface as a failed projection
// rather than a hung page.
const solutionEntitlementCallTimeout = 10 * time.Second

// solutionEntitlementPageSize is the page this gateway asks for. It pages until
// the authority stops handing back a cursor: the answer must be the viewer's whole
// entitled set, because a truncated one is indistinguishable to the frontend from
// "your organization installed less than it did".
const solutionEntitlementPageSize = 200

// solutionEntitlementPageLimit caps how many pages one request will follow, so a
// cursor that never terminates cannot pin this handler forever. It is far above
// any real organization's installed count; reaching it is a fault, answered as
// one rather than with a silently short list.
const solutionEntitlementPageLimit = 50

// solutionEntitlementClient is the authority surface this file consumes. The
// interface exists so the handler can be exercised without an accounts process.
type solutionEntitlementClient interface {
	List(ctx context.Context, req *accountsv1.ListSolutionEntitlementsRequest) (*accountsv1.ListSolutionEntitlementsResponse, error)
}

// accountsSolutionEntitlements invokes the entitlement read by method name over
// the existing accounts connection, presenting the gateway's cluster-internal
// credential — the same shape as accountsSolutionRegistry beside it, and for the
// same reason: there is no vendored client stub for the service, only the shared
// generated message types.
type accountsSolutionEntitlements struct {
	conn          *grpc.ClientConn
	internalToken string
}

func (c *accountsSolutionEntitlements) List(
	ctx context.Context, req *accountsv1.ListSolutionEntitlementsRequest,
) (*accountsv1.ListSolutionEntitlementsResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, solutionEntitlementCallTimeout)
	defer cancel()
	ctx = metadata.AppendToOutgoingContext(ctx, "x-codefly-internal-token", c.internalToken)
	resp := &accountsv1.ListSolutionEntitlementsResponse{}
	return resp, c.conn.Invoke(ctx, listSolutionEntitlementsMethod, req, resp)
}

// solutionEntitlementProjection is what a projection consumer reads. It carries
// the organization and viewer this gateway VERIFIED, not the ones anybody asked
// for, so a consumer can key a cache on them without having to trust its own
// request.
type solutionEntitlementProjection struct {
	Org    string                     `json:"org"`
	Viewer string                     `json:"viewer"`
	Usable []solutionEntitlementEntry `json:"solutions"`
}

type solutionEntitlementEntry struct {
	// The immutable solution TARGET this entitlement is for — the key a consumer
	// joins its own registered set against, after resolving whatever alias it
	// holds to the target currently serving it.
	//
	// It replaced a free-text identifier that was the route alias. An alias is
	// reusable, so joining on it let a replacement binding inherit the
	// predecessor's entitlement; the identity cannot be reused, so it cannot.
	TargetID string `json:"targetId"`
	// False when the installation is not healthy right now. A consumer shows such
	// a solution as unavailable rather than dropping it: the organization did
	// install it and the viewer was granted it, so hiding it would send someone
	// looking for a grant that exists. What it must not do is route it as healthy.
	Healthy bool `json:"healthy"`
	// The boundary that admitted it, so a consumer can say WHICH grant made a
	// solution visible without a second authority call.
	ScopeNodeID string `json:"scopeNodeId"`
}

// handleSolutionEntitlements answers what the calling viewer may use.
//
// Two credentials, each doing a different job. The cluster-internal token
// establishes that an in-cluster component is asking — the same gate the registry
// snapshot beside it carries. The caller's own bearer is what ext_authz verifies,
// and it is the only thing that decides WHOSE entitlements are answered.
//
// An unauthenticated caller gets 401 from that check, not an empty list. An empty
// projection would make "you are not signed in" and "your organization installed
// nothing" the same answer, and a consumer told the second goes looking at its
// registration instead of its session.
func (g *Gateway) handleSolutionEntitlements(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if g.authz == nil || !g.authz.acceptsInternalToken(r.Header.Get("X-Codefly-Internal-Token")) {
		w.Header().Set(solutionEntitlementRefusalHeader, refusalInternalCredential)
		httpError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	if g.solutionEntitlements == nil {
		// No authority client wired. Fail closed and distinguishably: an empty
		// projection here would retract every solution a viewer is currently
		// using, which is exactly the outage-as-deletion this surface must not do.
		httpError(w, http.StatusServiceUnavailable, "solution entitlement authority unavailable")
		return
	}

	// Same identity discipline as every protected route: drop caller-supplied
	// identity FIRST, so nothing below can read a header the caller wrote, then
	// run ext_authz and stamp what it returns.
	stripAllIdentityHeaders(r)
	checkResp, err := g.authz.Check(r.Context(), buildCheckRequest(r))
	if err != nil {
		httpError(w, http.StatusInternalServerError, "auth check failed")
		return
	}
	if denied := checkResp.GetDeniedResponse(); denied != nil {
		code := int(denied.GetStatus().GetCode())
		if code == 0 {
			code = http.StatusUnauthorized
		}
		httpError(w, code, denied.GetBody())
		return
	}
	if int(checkResp.GetStatus().GetCode()) != 0 {
		httpError(w, http.StatusForbidden, "forbidden")
		return
	}
	injectHeaders(r, checkResp.GetOkResponse().GetHeaders())

	// The verified identity, read from what the check just stamped — never from
	// the request as it arrived.
	org := r.Header.Get("X-Org-Id")
	viewer := effectiveViewer(r)
	if org == "" || viewer == "" {
		// Authenticated, but carrying no organization: a session that has not
		// selected one cannot have an org-scoped entitlement set. That is a
		// property of the credential, not an outage, and not an empty tenant's
		// menu either — so it is refused rather than answered with [].
		w.Header().Set(solutionEntitlementRefusalHeader, refusalNoOrganization)
		httpError(w, http.StatusForbidden, "no organization in this session")
		return
	}

	// Every read here fans into the authority — the grant union plus health for
	// each entitled installation — so it draws on the same per-org budget as the
	// equivalent catalog read. Without it this would be the one unmetered path
	// from a signed-in browser into accounts (the defect #513 closed for the
	// solution proxy): the menu polls, and nothing in front of it limits a poll.
	// The limiter keys on the X-Org-Id the check just stamped, and fails open on
	// a limiter-backend outage exactly as a StandardRead catalog route does.
	entry := &RouteEntry{
		Service:        "solution:_entitlements",
		UpstreamPath:   r.URL.Path,
		Protected:      true,
		RateLimitClass: edgeRateLimitClassStandardRead,
	}
	answer := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		entries, err := g.collectSolutionEntitlements(r.Context(), org, viewer)
		if err != nil {
			// The authority could not answer. 503, never an empty list: the
			// frontend distinguishes the two and holds its last projection rather
			// than emptying a working menu.
			httpError(w, http.StatusServiceUnavailable, "solution entitlement authority unavailable")
			return
		}
		writeSolutionJSON(w, http.StatusOK, solutionEntitlementProjection{
			Org: org, Viewer: viewer, Usable: entries,
		})
	})
	if g.rateLimiter == nil {
		answer.ServeHTTP(w, r)
		return
	}
	g.rateLimiter.Middleware(limiterFailureModeFor(entry), rateLimitClassFor(entry),
		entry.AuthenticationFactorAttempt, answer).ServeHTTP(w, r)
}

// effectiveViewer is whose entitlements the menu is for: the impersonated user
// while an impersonation is active, otherwise the caller.
//
// ext_authz stamps X-User-Id with the token's `sub`, which during impersonation
// stays the REAL actor, and names the user being viewed in X-Acting-As-User-Id.
// accounts authorizes every request against that effective subject
// (auth.RequestIdentity.EffectiveSubject), so a projection that read X-User-Id
// alone would show an impersonating administrator their own grants inside the
// impersonated user's organization — never the menu that user actually sees,
// which is the one thing impersonation is used to check.
func effectiveViewer(r *http.Request) string {
	if acting := r.Header.Get("X-Acting-As-User-Id"); acting != "" {
		return acting
	}
	return r.Header.Get("X-User-Id")
}

// collectSolutionEntitlements reads every page of the viewer's entitled set. A
// partial answer is not returned as a short list: the caller cannot tell it from
// a genuinely smaller set, and would narrow a menu on it.
func (g *Gateway) collectSolutionEntitlements(ctx context.Context, org, viewer string) ([]solutionEntitlementEntry, error) {
	entries := make([]solutionEntitlementEntry, 0)
	token := ""
	for page := 0; page < solutionEntitlementPageLimit; page++ {
		resp, err := g.solutionEntitlements.List(ctx, &accountsv1.ListSolutionEntitlementsRequest{
			OrgId:     org,
			SubjectId: viewer,
			PageSize:  solutionEntitlementPageSize,
			PageToken: token,
		})
		if err != nil {
			return nil, err
		}
		for _, entitlement := range resp.GetEntitlements() {
			entries = append(entries, solutionEntitlementEntry{
				TargetID:    entitlement.GetTargetId(),
				Healthy:     entitlement.GetHealthy(),
				ScopeNodeID: entitlement.GetRootScopeNodeId(),
			})
		}
		token = resp.GetNextPageToken()
		if token == "" {
			return entries, nil
		}
	}
	return nil, errSolutionEntitlementsUnbounded
}

// viewerSolutionAdmission is what the proxy decision needs from the authority:
// whether this organization installed this solution and this viewer was granted
// it. It is deliberately not a boolean — "not entitled" and "I could not ask"
// are different answers, and a proxy that collapsed them would route an
// uninstalled solution during an accounts outage or refuse an installed one.
type viewerSolutionAdmission int

const (
	// viewerSolutionAdmitted: the organization installed it and the viewer holds
	// a grant that reaches it.
	viewerSolutionAdmitted viewerSolutionAdmission = iota
	// viewerSolutionNotEntitled: the authority answered, and this solution is
	// not in the viewer's entitled set. A verdict, not an outage.
	viewerSolutionNotEntitled
	// viewerSolutionUndecidable: the authority could not be asked. Never routed
	// and never refused as a verdict — the caller answers 503.
	viewerSolutionUndecidable
)

// admitViewerSolution asks the authority whether the solution TARGET the
// request's resolution named is in the viewer's entitled set.
//
// The comparison is between identities, never between aliases. An alias is
// deliberately reusable — a withdrawn one may be claimed by another binding — so
// comparing the alias an installation named against the alias being requested
// admitted a REPLACEMENT binding to its predecessor's installation, forwarded
// the viewer's bearer to it, and exposed it to whoever had been granted the
// predecessor. A target is one continuous period of one binding's presence and
// is never reused, so the replacement resolves to its own target, which no
// installation of the predecessor names.
//
// The target is taken from the routing the CALLER resolved, not re-resolved
// here. That is the whole of it: this function used to call resolveTarget
// itself, so the identity it admitted came from a different read of a mutable
// snapshot than the address its caller then forwarded to, and the two could
// name different bindings. One resolution, passed in, cannot.
//
// A registration with no declaration resolves to no target and is admissible to
// NOBODY. That is deliberate: presence that nothing declared cannot be installed,
// so there is no organisation whose consent could admit it.
//
// It stops at the first match rather than reading every page: finding the id is
// a positive answer on its own, while NOT finding it is only sound after the
// whole set has been walked — so the short-circuit applies to exactly the
// direction where a partial read is conclusive. A cursor that never terminates
// is undecidable, not "not entitled", for the same reason the listing endpoint
// refuses a short answer: a truncated set is indistinguishable from a smaller
// one, and here that difference is whether traffic is refused.
//
// There is no cache, by the same reasoning #949 settled for the listing: the
// answer changes with no write at all — a grant's expires_at passes, a member
// leaves a team, a role loses a permission, an owner is demoted — so a cache
// keyed on any revision a write advances would route on an expired grant.
func (g *Gateway) admitViewerSolution(
	ctx context.Context, org, viewer string, routing *solutionRouting,
) viewerSolutionAdmission {
	targetID := routing.GetTargetID()
	if targetID == "" {
		// Declared by nothing: a self-registration no delivery opened a target
		// for. There is no identity to be entitled to, so this is a verdict
		// rather than an outage — it is not a presence an administrator could
		// have consented to, and no later read will make it one.
		return viewerSolutionNotEntitled
	}
	if g.solutionEntitlements == nil {
		// No authority client wired. Undecidable, never admitted: a deployment
		// that forgot to wire this must fail closed rather than serve every
		// solution to every organization, which is the behaviour this check
		// replaces.
		return viewerSolutionUndecidable
	}
	token := ""
	for page := 0; page < solutionEntitlementPageLimit; page++ {
		resp, err := g.solutionEntitlements.List(ctx, &accountsv1.ListSolutionEntitlementsRequest{
			OrgId:     org,
			SubjectId: viewer,
			PageSize:  solutionEntitlementPageSize,
			PageToken: token,
		})
		if err != nil {
			return viewerSolutionUndecidable
		}
		for _, entitlement := range resp.GetEntitlements() {
			// Membership of the entitled set is the whole test. Health is
			// deliberately NOT read here: an unhealthy installation is still
			// installed and still granted, and the registry's own resolve
			// already answers 503 for a registration that is not serving — so
			// reading Healthy here would turn one condition into two answers
			// and make "nobody granted you this" indistinguishable from "it is
			// restarting".
			if entitlement.GetTargetId() == targetID {
				return viewerSolutionAdmitted
			}
		}
		token = resp.GetNextPageToken()
		if token == "" {
			return viewerSolutionNotEntitled
		}
	}
	return viewerSolutionUndecidable
}
