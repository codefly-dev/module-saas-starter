package main

// Module credential exchanges and the solution upstream transport.

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"strings"
	"time"

	accountsv1 "auth-gateway/pkg/gen/saas/accounts/v1"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// isRuntimeRegisteredRoute selects the guarded transport — the one that
// re-validates the resolved IPs at dial time — for every upstream a DECLARATION
// named, rather than the static catalog.
//
// Both kinds qualify, for one reason: the address is read out of a delivered
// document and the person's identity is stamped on what is sent there, so the
// host must still refuse a name that resolves somewhere it will not send a
// request made on someone's behalf. A module route added without this line would
// have been the one declared upstream dialled on the plain transport, and nothing
// about the route's shape would have said so.
//
// The predicate has a second reader now: proxyTo strips the person's session
// credential for exactly these routes (removePersonsSessionCredential), so what
// reaches a declared upstream is the stamped identity and not a bearer.
func isRuntimeRegisteredRoute(entry *RouteEntry) bool {
	return entry != nil &&
		(strings.HasPrefix(entry.Service, solutionServicePrefix) ||
			strings.HasPrefix(entry.Service, moduleServicePrefix))
}

// meshHostSuffixes are the DNS suffixes that denote a composition-local
// (cluster/mesh) upstream. A hostname ending in one of these is treated as
// mesh-internal and allowed.
//
// ".localhost" is deliberately NOT here: a bare "localhost" is already allowed
// by the single-label rule below, and Go's resolver does not RFC-6761
// special-case "*.localhost" to loopback — it does a real DNS lookup. Listing
// the suffix would admit multi-label names like "evil.localhost" that resolve
// wherever their DNS points, for no legitimate mesh use.
var meshHostSuffixes = []string{
	".local", ".internal", ".svc", ".cluster.local",
}

// isDisallowedRegisteredUpstreamHost validates a declared upstream read from
// the registry before the gateway forwards user credentials to it. It extends
// isForbiddenUpstreamHost,
// which it composes: beyond those SSRF sinks, a declared upstream must be
// composition-local, so a public IP or an external dotted FQDN is also rejected.
// Allowed: loopback, RFC1918/ULA private IPs, bare single-label service names
// (including "localhost"), and cluster-suffixed names.
func isDisallowedRegisteredUpstreamHost(host string) bool {
	h := strings.ToLower(strings.TrimSpace(host))
	// Credential-theft SSRF sinks (unspecified/link-local/metadata) and empty.
	if isForbiddenUpstreamHost(h) {
		return true
	}
	if ip := net.ParseIP(h); ip != nil {
		// Loopback and private-range addresses are mesh-local; any other
		// (globally routable) IP is not composition-local and is rejected.
		if ip.IsLoopback() || ip.IsPrivate() {
			return false
		}
		return true
	}
	// A bare single-label name (no dot) is a mesh/service short name — allow it
	// (this also covers "localhost").
	if !strings.Contains(h, ".") {
		return false
	}
	// A dotted name is allowed only when it carries a known cluster suffix; a
	// public FQDN like api.example.com is rejected.
	for _, suffix := range meshHostSuffixes {
		if strings.HasSuffix(h, suffix) {
			return false
		}
	}
	return true
}

// modulePrefix is the path prefix of the module credential exchanges. They run
// before routing because the internal-token header they authenticate on has not
// been stripped yet, so a capability presented to one of them is verified against
// this prefix rather than against a resolved route.
const modulePrefix = "/modules/"

// moduleSecretHeader carries the module identity secret to accounts.
const moduleSecretHeader = "X-Codefly-Module-Secret"

// ErrorInfo domain shared with accounts source-delegation refusals.
const solutionRegistryErrorDomain = "accounts.saas.codefly.dev"

// moduleExchangeTimeout bounds the accounts leg of a credential exchange.
const moduleExchangeTimeout = 10 * time.Second

// moduleWorkContextPath serves the identity exchange a module runs at startup: it
// presents its identity secret and receives the Work Context its
// service principal calls the module-facing capability surface with.
const moduleWorkContextPath = "/modules/_work-context"

// mintModuleWorkContextMethod is accounts' Work Context mint for a composed
// module. Its EXPOSURE_INTERNAL policy means the generated
// mesh policy admits this gateway's service account and denies every other.
const mintModuleWorkContextMethod = "/saas.accounts.v1.ModuleCapabilitiesService/MintModuleWorkContext"

// handleModuleWorkContext serves POST /modules/_work-context. It returns true
// when it has handled the request.
//
// It brokers for the same reason the credential exchange does: a composed module
// cannot reach accounts' internal listener itself, and this handler makes no
// authorization decision — accounts decides which principal the presented secret
// is good for and which tenant it may act on.
// logModuleExchangeRefusal records WHY accounts refused a module exchange, where
// the client is told only that it was refused.
//
// Every branch below answers a fixed, reasonless string — "unauthorized",
// "forbidden" — and that is right: these endpoints are reached by a module
// presenting its own secret, and the reply must not tell a caller which of the
// composition's declarations it fell foul of. But accounts names the fault
// exactly ("owner is not allowed <resource>:<action> at requested scope", an
// unknown binding, a prefix whose grant is missing), and dropping it left the
// gateway with no record at all: a composition whose MODULE_PRINCIPALS entry is
// wrong produced a bare 403 here and silence in the log, which is how a
// deployed module's refusal became undiagnosable. gRPC status messages carry no
// credential — the secret never reaches accounts' error.
func logModuleExchangeRefusal(exchange, prefix string, err error) {
	log.Printf("WARN: module exchange refused: exchange=%s prefix=%s code=%s reason=%q",
		exchange, prefix, status.Code(err), status.Convert(err).Message())
}

func (g *Gateway) handleModuleWorkContext(w http.ResponseWriter, r *http.Request) bool {
	if r.URL.Path != moduleWorkContextPath {
		return false
	}
	if r.Method != http.MethodPost {
		httpError(w, http.StatusMethodNotAllowed, "method not allowed")
		return true
	}

	if g.authz == nil || !g.authz.acceptsInternalToken(r.Header.Get("X-Codefly-Internal-Token")) {
		httpError(w, http.StatusUnauthorized, "unauthorized")
		return true
	}
	secret := r.Header.Get(moduleSecretHeader)
	if secret == "" {
		httpError(w, http.StatusUnauthorized, "unauthorized")
		return true
	}

	var payload struct {
		Prefix string `json:"prefix"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&payload); err != nil {
		httpError(w, http.StatusBadRequest, "invalid json")
		return true
	}
	if !validCatalogIdentity(payload.Prefix) {
		httpError(w, http.StatusBadRequest, "invalid prefix")
		return true
	}

	ctx, cancel := context.WithTimeout(r.Context(), moduleExchangeTimeout)
	defer cancel()
	issued, err := g.authz.mintModuleWorkContext(ctx, payload.Prefix, secret)
	if err != nil {
		logModuleExchangeRefusal("module-work-context", payload.Prefix, err)
		switch status.Code(err) {
		case codes.PermissionDenied:
			httpError(w, http.StatusUnauthorized, "unauthorized")
		case codes.InvalidArgument:
			httpError(w, http.StatusBadRequest, "invalid request")
		default:
			httpError(w, http.StatusBadGateway, "work context unavailable")
		}
		return true
	}

	body, err := json.Marshal(map[string]string{
		"token":       issued.GetToken(),
		"expiresAt":   issued.GetExpiresAt().AsTime().UTC().Format(time.RFC3339),
		"principalId": issued.GetPrincipalId(),
		"tenant":      issued.GetTenant(),
	})
	if err != nil {
		httpError(w, http.StatusBadGateway, "work context unavailable")
		return true
	}
	w.Header().Set("content-type", "application/json")
	w.Header().Set("cache-control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
	return true
}

// moduleOperationContextPath serves the headless operation exchange: a module
// presents its identity secret and one of its installed operation bindings, and
// receives a short-lived Work Context addressed to that binding's audience, for
// work no person is present for.
const moduleOperationContextPath = "/modules/_operation-context"

// mintModuleOperationContextMethod is accounts' headless operation mint.
// EXPOSURE_INTERNAL like the two exchanges above.
const mintModuleOperationContextMethod = "/saas.accounts.v1.ModuleCapabilitiesService/MintModuleOperationContext"

// handleModuleOperationContext serves POST /modules/_operation-context. It
// returns true when it has handled the request.
//
// It brokers exactly as handleModuleWorkContext does, on the same perimeter
// (cluster-internal token plus the module's identity secret), and decides
// nothing: accounts alone knows which bindings a module declared and which of
// them may be minted with no person present. Unlike that exchange the outcome
// has two refusals worth telling apart — an unproven module (401) and a proven
// module naming a binding it may not mint headless (403).
func (g *Gateway) handleModuleOperationContext(w http.ResponseWriter, r *http.Request) bool {
	if r.URL.Path != moduleOperationContextPath {
		return false
	}
	if r.Method != http.MethodPost {
		httpError(w, http.StatusMethodNotAllowed, "method not allowed")
		return true
	}

	if g.authz == nil || !g.authz.acceptsInternalToken(r.Header.Get("X-Codefly-Internal-Token")) {
		httpError(w, http.StatusUnauthorized, "unauthorized")
		return true
	}
	secret := r.Header.Get(moduleSecretHeader)
	if secret == "" {
		httpError(w, http.StatusUnauthorized, "unauthorized")
		return true
	}

	var payload struct {
		Prefix  string `json:"prefix"`
		Binding string `json:"binding"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&payload); err != nil {
		httpError(w, http.StatusBadRequest, "invalid json")
		return true
	}
	if !validCatalogIdentity(payload.Prefix) {
		httpError(w, http.StatusBadRequest, "invalid prefix")
		return true
	}
	if payload.Binding == "" || len(payload.Binding) > 128 {
		httpError(w, http.StatusBadRequest, "invalid binding")
		return true
	}

	ctx, cancel := context.WithTimeout(r.Context(), moduleExchangeTimeout)
	defer cancel()
	issued, err := g.authz.mintModuleOperationContext(ctx, payload.Prefix, secret, payload.Binding)
	if err != nil {
		logModuleExchangeRefusal("module-operation-context/"+payload.Binding, payload.Prefix, err)
		switch status.Code(err) {
		case codes.Unauthenticated:
			httpError(w, http.StatusUnauthorized, "unauthorized")
		case codes.PermissionDenied:
			httpError(w, http.StatusForbidden, "forbidden")
		case codes.InvalidArgument:
			httpError(w, http.StatusBadRequest, "invalid request")
		default:
			httpError(w, http.StatusBadGateway, "operation context unavailable")
		}
		return true
	}

	// The field names are this exchange's own wire contract with its consumers
	// (snake_case, `work_context` rather than a bare `token`): the capability is
	// a Work Context for another service, not a credential for this gateway.
	body, err := json.Marshal(map[string]string{
		"work_context": issued.GetToken(),
		"expires_at":   issued.GetExpiresAt().AsTime().UTC().Format(time.RFC3339),
		"principal_id": issued.GetPrincipalId(),
		"tenant":       issued.GetTenant(),
		"audience":     issued.GetAudience(),
		"binding":      issued.GetBinding(),
	})
	if err != nil {
		httpError(w, http.StatusBadGateway, "operation context unavailable")
		return true
	}
	w.Header().Set("content-type", "application/json")
	w.Header().Set("cache-control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
	return true
}

func (s *ExtAuthz) mintModuleWorkContext(
	ctx context.Context, prefix, secret string,
) (*accountsv1.ModuleMintWorkContextResponse, error) {
	if s.backendConn == nil {
		return nil, fmt.Errorf("accounts connection not configured")
	}
	ctx = metadata.AppendToOutgoingContext(ctx, "x-codefly-internal-token", s.internalToken)
	response := &accountsv1.ModuleMintWorkContextResponse{}
	request := &accountsv1.ModuleMintWorkContextRequest{Prefix: prefix, Secret: secret}
	if err := s.backendConn.Invoke(ctx, mintModuleWorkContextMethod, request, response); err != nil {
		return nil, err
	}
	return response, nil
}

func (s *ExtAuthz) mintModuleOperationContext(
	ctx context.Context, prefix, secret, binding string,
) (*accountsv1.ModuleMintOperationContextResponse, error) {
	if s.backendConn == nil {
		return nil, fmt.Errorf("accounts connection not configured")
	}
	ctx = metadata.AppendToOutgoingContext(ctx, "x-codefly-internal-token", s.internalToken)
	response := &accountsv1.ModuleMintOperationContextResponse{}
	request := &accountsv1.ModuleMintOperationContextRequest{Prefix: prefix, Secret: secret, Binding: binding}
	if err := s.backendConn.Invoke(ctx, mintModuleOperationContextMethod, request, response); err != nil {
		return nil, err
	}
	return response, nil
}

// moduleSourceOperationContextPath serves the source-delegation exchange: a
// module presents its identity secret and a delegation — by id, or by the
// source whose active delegation to the module is meant — and receives the
// short-lived Work Context that source's sync runs with, owned by the person
// who connected the source.
const moduleSourceOperationContextPath = "/modules/_source-operation-context"

// mintSourceOperationContextMethod is accounts' source-delegation mint.
// EXPOSURE_INTERNAL like the other module exchanges.
const mintSourceOperationContextMethod = "/saas.accounts.v1.ModuleCapabilitiesService/MintSourceOperationContext"

// sourceDelegationMissingReason is the google.rpc.ErrorInfo reason accounts
// attaches when the source has no active delegation to the module
// (adapters.SourceDelegationMissingReason, under the domain the solution
// registry's refusals use). The two strings are a wire contract; a test on
// each side pins them.
const sourceDelegationMissingReason = "DELEGATION_MISSING"

// sourceDelegationRevokedReason and sourceDelegationInvalidReason are the
// ErrorInfo reasons accounts attaches to a PermissionDenied refusal of a
// delegation (adapters.SourceDelegationRevokedReason and
// adapters.SourceDelegationInvalidReason). The 403 carries them so a module can
// tell "the person withdrew it" from "no delegation this module may use" —
// accounts has already chosen to reveal that much; the gateway relays exactly
// these two and nothing else, so an unrecognised refusal stays a bare
// `forbidden`.
const (
	sourceDelegationRevokedReason = "DELEGATION_REVOKED"
	sourceDelegationInvalidReason = "DELEGATION_INVALID"
)

// handleModuleSourceOperationContext serves POST
// /modules/_source-operation-context. It returns true when it has handled the
// request.
//
// It brokers exactly as handleModuleOperationContext does, on the same
// perimeter (cluster-internal token plus the module's identity secret), and
// decides nothing: accounts alone holds the delegation and re-checks it. Three
// refusals are worth telling apart, and the status says which:
//   - 401: the module is not proven;
//   - 412: the source has no active delegation to the module — a person must
//     connect or reconnect it (body `DELEGATION_MISSING`);
//   - 403: the delegation named is revoked (body `DELEGATION_REVOKED`), or not
//     usable by this module (body `DELEGATION_INVALID`); any other refusal is a
//     bare `forbidden`.
func (g *Gateway) handleModuleSourceOperationContext(w http.ResponseWriter, r *http.Request) bool {
	if r.URL.Path != moduleSourceOperationContextPath {
		return false
	}
	if r.Method != http.MethodPost {
		httpError(w, http.StatusMethodNotAllowed, "method not allowed")
		return true
	}

	if g.authz == nil || !g.authz.acceptsInternalToken(r.Header.Get("X-Codefly-Internal-Token")) {
		httpError(w, http.StatusUnauthorized, "unauthorized")
		return true
	}
	secret := r.Header.Get(moduleSecretHeader)
	if secret == "" {
		httpError(w, http.StatusUnauthorized, "unauthorized")
		return true
	}

	var payload struct {
		Prefix       string `json:"prefix"`
		DelegationID string `json:"delegation_id"`
		SourceID     string `json:"source_id"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&payload); err != nil {
		httpError(w, http.StatusBadRequest, "invalid json")
		return true
	}
	if !validCatalogIdentity(payload.Prefix) {
		httpError(w, http.StatusBadRequest, "invalid prefix")
		return true
	}
	request := &accountsv1.ModuleMintSourceOperationContextRequest{Prefix: payload.Prefix, Secret: secret}
	switch {
	case payload.DelegationID != "" && payload.SourceID == "" && len(payload.DelegationID) <= 64:
		request.Delegation = &accountsv1.ModuleMintSourceOperationContextRequest_DelegationId{DelegationId: payload.DelegationID}
	case payload.SourceID != "" && payload.DelegationID == "" && len(payload.SourceID) <= 64:
		request.Delegation = &accountsv1.ModuleMintSourceOperationContextRequest_SourceId{SourceId: payload.SourceID}
	default:
		httpError(w, http.StatusBadRequest, "exactly one of delegation_id or source_id is required")
		return true
	}

	ctx, cancel := context.WithTimeout(r.Context(), moduleExchangeTimeout)
	defer cancel()
	issued, err := g.authz.mintSourceOperationContext(ctx, request)
	if err != nil {
		logModuleExchangeRefusal("source-operation-context", request.GetPrefix(), err)
		switch status.Code(err) {
		case codes.Unauthenticated:
			httpError(w, http.StatusUnauthorized, "unauthorized")
		case codes.PermissionDenied:
			switch reason := sourceDelegationReason(err); reason {
			case sourceDelegationRevokedReason, sourceDelegationInvalidReason:
				httpError(w, http.StatusForbidden, reason)
			default:
				httpError(w, http.StatusForbidden, "forbidden")
			}
		case codes.InvalidArgument:
			httpError(w, http.StatusBadRequest, "invalid request")
		case codes.FailedPrecondition:
			if sourceDelegationReason(err) == sourceDelegationMissingReason {
				httpError(w, http.StatusPreconditionFailed, sourceDelegationMissingReason)
				return true
			}
			httpError(w, http.StatusBadGateway, "source operation context unavailable")
		default:
			httpError(w, http.StatusBadGateway, "source operation context unavailable")
		}
		return true
	}

	// The same snake_case shape as /modules/_operation-context, plus what this
	// exchange adds: whose authority the context carries and which delegation
	// and source it was minted from.
	body, err := json.Marshal(map[string]string{
		"work_context":       issued.GetToken(),
		"expires_at":         issued.GetExpiresAt().AsTime().UTC().Format(time.RFC3339),
		"principal_id":       issued.GetPrincipalId(),
		"owner_principal_id": issued.GetOwnerPrincipalId(),
		"tenant":             issued.GetTenant(),
		"audience":           issued.GetAudience(),
		"binding":            issued.GetBinding(),
		"delegation_id":      issued.GetDelegationId(),
		"source_id":          issued.GetSourceId(),
	})
	if err != nil {
		httpError(w, http.StatusBadGateway, "source operation context unavailable")
		return true
	}
	w.Header().Set("content-type", "application/json")
	w.Header().Set("cache-control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
	return true
}

// sourceDelegationReason returns the reason of the first google.rpc.ErrorInfo
// under accounts' domain, or "" when there is none. It reads the structured
// detail, never the message; the caller decides which reasons it relays.
func sourceDelegationReason(err error) string {
	for _, detail := range status.Convert(err).Details() {
		if info, ok := detail.(*errdetails.ErrorInfo); ok &&
			info.GetDomain() == solutionRegistryErrorDomain {
			return info.GetReason()
		}
	}
	return ""
}

func (s *ExtAuthz) mintSourceOperationContext(
	ctx context.Context, request *accountsv1.ModuleMintSourceOperationContextRequest,
) (*accountsv1.ModuleMintSourceOperationContextResponse, error) {
	if s.backendConn == nil {
		return nil, fmt.Errorf("accounts connection not configured")
	}
	ctx = metadata.AppendToOutgoingContext(ctx, "x-codefly-internal-token", s.internalToken)
	response := &accountsv1.ModuleMintSourceOperationContextResponse{}
	if err := s.backendConn.Invoke(ctx, mintSourceOperationContextMethod, request, response); err != nil {
		return nil, err
	}
	return response, nil
}

// --- Resolve-time SSRF / DNS-rebinding defense for module upstreams ---

// moduleResolver is the DNS surface the module dial guard needs. *net.Resolver
// satisfies it; tests inject a stub to exercise rebinding deterministically.
type moduleResolver interface {
	LookupIPAddr(ctx context.Context, host string) ([]net.IPAddr, error)
}

// newModuleUpstreamTransport returns a reverse-proxy transport that re-validates
// a solution upstream's RESOLVED address at dial time. The registry read checks
// the mesh host string (isDisallowedRegisteredUpstreamHost);
// but a name it controls can resolve off-mesh at proxy time (DNS rebinding). This
// transport resolves the host, rejects the dial unless every resolved address is
// mesh-local, then connects to a validated IP directly — never re-resolving — so
// the address dialed is exactly the one just checked.
func newModuleUpstreamTransport(resolver moduleResolver) *http.Transport {
	base := http.DefaultTransport.(*http.Transport).Clone()
	// The resolve-and-pin DialContext below is the ONLY sanctioned path to a
	// module upstream. The cloned DefaultTransport inherits Proxy:
	// ProxyFromEnvironment, which would defeat that: with HTTP(S)_PROXY set the
	// transport dials the PROXY, so DialContext would validate the proxy's
	// address instead of the upstream's, and in-mesh module traffic would be
	// tunnelled through an arbitrary egress host — the exact off-mesh reach this
	// guard exists to prevent. A federated module upstream is always mesh-local
	// and must never be proxied, so disable proxying and keep the validated
	// direct dial authoritative.
	base.Proxy = nil
	dialer := &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}
	base.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, err
		}
		addrs, err := resolver.LookupIPAddr(ctx, host)
		if err != nil {
			return nil, err
		}
		if err := validateResolvedModuleAddrs(addrs); err != nil {
			return nil, err
		}
		// Pin the connection to an address we just validated: dial the resolved
		// IPs directly rather than re-resolving the hostname, so a DNS answer that
		// changes between the check and the connect cannot slip an off-mesh
		// address past the guard.
		var lastErr error
		for _, a := range addrs {
			conn, err := dialer.DialContext(ctx, network, net.JoinHostPort(a.IP.String(), port))
			if err == nil {
				return conn, nil
			}
			lastErr = err
		}
		return nil, lastErr
	}
	return base
}

// validateResolvedModuleAddrs fails closed unless every resolved address is
// mesh-local. An empty result, or any single off-mesh address in the set,
// rejects the whole dial: a rebinding answer that mixes a benign and a forbidden
// IP must not earn a retry to the forbidden one.
func validateResolvedModuleAddrs(addrs []net.IPAddr) error {
	if len(addrs) == 0 {
		return fmt.Errorf("module upstream did not resolve to any address")
	}
	for _, a := range addrs {
		if !isAllowedResolvedModuleIP(a.IP) {
			return fmt.Errorf("forbidden upstream address: %s", a.IP)
		}
	}
	return nil
}

// isAllowedResolvedModuleIP is the resolve-time counterpart to the registry-read
// host-string guard (isDisallowedRegisteredUpstreamHost): it re-checks the actual
// address a module hostname resolved to. Only loopback and private (RFC1918 /
// ULA) ranges are composition-local; the unspecified, link-local (covering the
// 169.254.169.254 cloud-metadata IP), and every globally routable address are
// rejected.
func isAllowedResolvedModuleIP(ip net.IP) bool {
	if ip == nil || ip.IsUnspecified() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
		return false
	}
	return ip.IsLoopback() || ip.IsPrivate()
}
