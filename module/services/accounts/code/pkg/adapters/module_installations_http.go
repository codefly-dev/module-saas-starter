package adapters

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"accounts/pkg/business"

	codefly "github.com/codefly-dev/sdk-go"
)

const moduleInstallationPrefix = "/v1/module-installations/"

type moduleInstallationService interface {
	ModuleAuthorizeWorkContext(string, string) (business.ModuleWorkContextAuthority, error)
	VerifyModuleTenant(context.Context, business.ModuleWorkContextAuthority) error
	RecordModuleWorkContextMint(context.Context, string, business.ModuleWorkContextAuthority) error
	ReconcileModuleInstallation(context.Context, business.ModuleCaller, *business.InstallerPolicy, business.ModuleInstallationRequest, bool) (*business.ModuleInstallationResult, error)
}

type ModuleInstallationHTTPHandler struct {
	service moduleInstallationService
	policy  func() (*business.InstallerPolicy, error)
	verify  func(string) (business.ModuleCaller, error)
	mint    func(business.ModuleWorkContextAuthority) (string, time.Time, error)
}

// NewModuleInstallationHTTPHandler uses the existing module identity issuer and
// verifier. The root-owned projected policy is reread per request, so expiry,
// deletion and narrowing revoke even a previously minted Work Context.
func NewModuleInstallationHTTPHandler(service *business.Service, policyPath string) (http.Handler, error) {
	load := func() (*business.InstallerPolicy, error) {
		f, err := os.Open(policyPath)
		if err != nil {
			return nil, err
		}
		defer f.Close()
		return business.ParseInstallerPolicy(f)
	}
	if _, err := load(); err != nil {
		return nil, err
	}
	return &ModuleInstallationHTTPHandler{service: service, policy: load,
		verify: func(token string) (business.ModuleCaller, error) {
			return WorkContextSingleton().VerifyModuleWorkContext(token)
		},
		mint: func(a business.ModuleWorkContextAuthority) (string, time.Time, error) {
			token, claims, err := WorkContextSingleton().StartModuleTask(a)
			if err != nil {
				return "", time.Time{}, err
			}
			return token.Encoded(), time.Unix(claims.GetExpiresAtUnix(), 0).UTC(), nil
		},
	}, nil
}

func installerJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
func installerError(w http.ResponseWriter, status int, message string) {
	installerJSON(w, status, map[string]string{"error": message})
}
func installerDecode(w http.ResponseWriter, r *http.Request, out any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(out); err != nil {
		installerError(w, http.StatusBadRequest, "invalid request body")
		return false
	}
	if d.Decode(new(any)) != io.EOF {
		installerError(w, http.StatusBadRequest, "request body has trailing data")
		return false
	}
	return true
}
func (h *ModuleInstallationHTTPHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		installerError(w, http.StatusMethodNotAllowed, "POST required")
		return
	}
	mode := strings.TrimPrefix(r.URL.Path, moduleInstallationPrefix)
	if mode != "token" && mode != "inspect" && mode != "apply" && mode != "verify" {
		installerError(w, http.StatusNotFound, "unknown installer operation")
		return
	}
	policy, err := h.policy()
	if err != nil {
		installerError(w, http.StatusServiceUnavailable, "installer policy unavailable")
		return
	}
	if mode == "token" {
		h.token(w, r, policy)
		return
	}
	headers := r.Header.Values(codefly.WorkContextHeaderName)
	if len(headers) != 1 || headers[0] == "" {
		installerError(w, http.StatusUnauthorized, "module work context required")
		return
	}
	caller, err := h.verify(headers[0])
	if err != nil {
		installerError(w, http.StatusUnauthorized, "invalid or expired module work context")
		return
	}
	var req business.ModuleInstallationRequest
	if !installerDecode(w, r, &req) {
		return
	}
	if req.OrganizationSlug == "" || req.ModuleID == "" || req.AgentIdentifier == "" {
		installerError(w, http.StatusBadRequest, "organizationSlug, moduleId and agentIdentifier are required")
		return
	}
	result, err := h.service.ReconcileModuleInstallation(r.Context(), caller, policy, req, mode == "apply")
	if err != nil {
		if errors.Is(err, business.ErrInstallerDenied) {
			installerError(w, http.StatusForbidden, "installer delegation denied")
			return
		}
		var se *business.StoreError
		if errors.As(err, &se) && (se.StoreErrorType == business.ErrTypeConflict || se.StoreErrorType == business.ErrTypeNotFound) {
			installerError(w, http.StatusConflict, se.Error())
			return
		}
		installerError(w, http.StatusServiceUnavailable, "installation prerequisite or persistence unavailable")
		return
	}
	if mode == "verify" && result.State != "ready" {
		installerError(w, http.StatusConflict, "installation is absent; apply the approved declaration")
		return
	}
	installerJSON(w, http.StatusOK, result)
}
func (h *ModuleInstallationHTTPHandler) token(w http.ResponseWriter, r *http.Request, policy *business.InstallerPolicy) {
	var req struct {
		Prefix string `json:"prefix"`
		Secret string `json:"secret"`
	}
	if !installerDecode(w, r, &req) {
		return
	}
	authority, err := h.service.ModuleAuthorizeWorkContext(req.Prefix, req.Secret)
	if err != nil || !policy.AllowsIdentity(business.ModuleCaller{PrincipalID: authority.PrincipalID, BoundOrg: authority.Tenant}, time.Now()) {
		installerError(w, http.StatusForbidden, "installer identity denied")
		return
	}
	if err = h.service.VerifyModuleTenant(r.Context(), authority); err != nil {
		installerError(w, http.StatusServiceUnavailable, "installer organization prerequisite unavailable")
		return
	}
	token, expires, err := h.mint(authority)
	if err != nil {
		installerError(w, http.StatusServiceUnavailable, "installer identity issuer unavailable")
		return
	}
	if err = h.service.RecordModuleWorkContextMint(r.Context(), req.Prefix, authority); err != nil {
		installerError(w, http.StatusServiceUnavailable, "installer audit unavailable")
		return
	}
	installerJSON(w, http.StatusOK, map[string]any{"token": token, "expiresAt": expires, "principalId": authority.PrincipalID, "tenant": authority.Tenant})
}
