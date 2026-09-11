package adapters

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"accounts/pkg/auth"
	"accounts/pkg/business"
	wire "accounts/pkg/executioncustody"
	gen "accounts/pkg/gen/saas/accounts/v1"

	base "github.com/codefly-dev/core/generated/go/codefly/base/v0"
	codefly "github.com/codefly-dev/sdk-go"
	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

// ExecutionConsumerPolicy is immutable deployment configuration, never a
// request-selected scope ceiling. Changing it fences all existing bindings.
type ExecutionConsumerPolicy struct {
	TaskResourceKind string
	TaskActions      []string
	WorkerURI        string
	ParentAudience   string
	TaskAudience     string
	Audience         string
	Profile          string
	ResourceKind     string
	ResourceID       string
	InvokeAction     string
	ReadAction       string
}

type ExecutionCustodyConfig struct {
	Authority *WorkContextAuthorityServer
	Minter    auth.JWTMinter
	Store     business.ExecutionCustodyStore
	Cipher    business.SecretCipher
	Consumers map[string]ExecutionConsumerPolicy
}

type executionCustody struct{ config ExecutionCustodyConfig }

type custodyPayload struct {
	Request      wire.RegisterRequest
	RealActor    string
	Delegation   *auth.Actor
	TaskToken    string
	ClaimsDigest string
	PolicyDigest string
}

// NewExecutionCustodyServer builds an opt-in private listener. The owning host
// calls ServeTLS with this server's configured certificate. It must not wrap
// the handler in a body logger or mount it on the public gateway. Client roots
// authenticate workers; owner admission instead uses the real Accounts JWT.
func NewExecutionCustodyServer(config ExecutionCustodyConfig, tlsConfig *tls.Config) (*http.Server, error) {
	if config.Authority == nil || config.Authority.configureErr != nil || config.Authority.verifier == nil || config.Minter == nil || config.Store == nil || config.Cipher == nil || len(config.Consumers) == 0 || tlsConfig == nil || tlsConfig.ClientCAs == nil || len(tlsConfig.Certificates) == 0 {
		return nil, errors.New("execution custody dependencies and TLS identities required")
	}
	consumers := make(map[string]ExecutionConsumerPolicy, len(config.Consumers))
	for name, p := range config.Consumers {
		u, err := url.Parse(p.WorkerURI)
		if name == "" || err != nil || u.Scheme != "spiffe" || u.Host == "" || u.Path == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || p.ParentAudience == "" || p.TaskAudience == "" || p.Audience == "" || p.ParentAudience == p.TaskAudience || p.ParentAudience == p.Audience || p.TaskAudience == p.Audience || p.Profile == "" || p.ResourceKind == "" || p.ResourceID == "" || p.InvokeAction == "" || p.ReadAction == "" || p.InvokeAction == p.ReadAction {
			return nil, errors.New("invalid execution consumer policy")
		}
		if p.TaskResourceKind == "" || len(p.TaskActions) == 0 {
			return nil, errors.New("task scope policy required")
		}
		p.TaskActions = append([]string(nil), p.TaskActions...)
		consumers[name] = p
	}
	config.Consumers = consumers
	b := &executionCustody{config: config}
	tc := tlsConfig.Clone()
	tc.MinVersion = tls.VersionTLS13
	tc.ClientAuth = tls.VerifyClientCertIfGiven
	tc.InsecureSkipVerify = false
	tc.GetConfigForClient = nil
	return &http.Server{Handler: b, TLSConfig: tc, ReadHeaderTimeout: 3 * time.Second, ReadTimeout: 5 * time.Second, WriteTimeout: 5 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 16 << 10}, nil
}

func (b *executionCustody) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodPost {
		custodyError(w, status.Error(codes.InvalidArgument, ""))
		return
	}
	if r.TLS == nil {
		custodyError(w, status.Error(codes.Unauthenticated, ""))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 4*time.Second)
	defer cancel()
	r = r.WithContext(ctx)
	var result any
	var err error
	switch r.URL.Path {
	case wire.RegisterPath, wire.RecoverPath:
		var identity *auth.Identity
		header := r.Header.Get("Authorization")
		if strings.HasPrefix(header, "Bearer ") {
			identity, err = b.config.Minter.VerifyAccess(strings.TrimPrefix(header, "Bearer "))
		}
		if errors.Is(err, auth.ErrRevocationUnavailable) {
			err = status.Error(codes.Unavailable, "")
		} else if err != nil || identity == nil {
			err = status.Error(codes.Unauthenticated, "")
		}

		if err == nil {
			if r.URL.Path == wire.RecoverPath {
				var in wire.RecoverRequest
				if decodeCustody(w, r, &in) != nil {
					err = status.Error(codes.InvalidArgument, "")
				} else {
					result, err = b.recoverRegistration(ctx, identity, in)
				}
			} else {
				var in wire.RegisterRequest
				if decodeCustody(w, r, &in) != nil {
					err = status.Error(codes.InvalidArgument, "")
				} else {
					result, err = b.register(ctx, identity, in)
				}
			}
		}
	case wire.ExchangePath:
		worker := ""
		if len(r.TLS.VerifiedChains) > 0 && len(r.TLS.VerifiedChains[0]) > 0 {
			leaf := r.TLS.VerifiedChains[0][0]
			if len(leaf.URIs) == 1 && time.Now().Before(leaf.NotAfter) && !time.Now().Before(leaf.NotBefore) {
				worker = leaf.URIs[0].String()
			}
		}
		if worker == "" {
			err = status.Error(codes.Unauthenticated, "")
		} else {
			var in wire.ExchangeRequest
			if decodeCustody(w, r, &in) != nil {
				err = status.Error(codes.InvalidArgument, "")
			} else {
				result, err = b.exchange(ctx, worker, in)
			}
		}
	default:
		err = status.Error(codes.NotFound, "")
	}
	if err != nil {
		custodyError(w, err)
		return
	}
	if ctx.Err() != nil {
		custodyError(w, status.Error(codes.Unavailable, ""))
		return
	}
	_ = json.NewEncoder(w).Encode(result)
}

func decodeCustody(w http.ResponseWriter, r *http.Request, out any) error {
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 128<<10))
	d.DisallowUnknownFields()
	if err := d.Decode(out); err != nil {
		return err
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return errors.New("trailing data")
	}
	return nil
}
func custodyError(w http.ResponseWriter, err error) {
	code := status.Code(err)
	httpCode := http.StatusServiceUnavailable
	switch code {
	case codes.InvalidArgument:
		httpCode = 400
	case codes.Unauthenticated:
		httpCode = 401
	case codes.PermissionDenied:
		httpCode = 403
	case codes.NotFound:
		httpCode = 404
	case codes.AlreadyExists:
		httpCode = 409
	case codes.FailedPrecondition:
		httpCode = 412
	default:
		code = codes.Unavailable
	}
	w.WriteHeader(httpCode)
	_ = json.NewEncoder(w).Encode(wire.Error{Code: code.String()})
}
func custodyHash(data []byte) string { v := sha256.Sum256(data); return hex.EncodeToString(v[:]) }
func custodyJSONHash(v any) string   { data, _ := json.Marshal(v); return custodyHash(data) }
func validCustodyBinding(v wire.Binding) bool {
	for _, id := range []string{v.OrgID, v.OwnerID} {
		u, err := uuid.Parse(id)
		if err != nil || u == uuid.Nil || u.String() != id {
			return false
		}
	}
	for _, d := range []string{v.IntentDigest} {
		raw, err := hex.DecodeString(d)
		if err != nil || len(raw) != 32 || strings.ToLower(d) != d {
			return false
		}
	}
	if len(v.AdmissionID) < 1 || len(v.AdmissionID) > 128 {
		return false
	}
	for _, c := range v.AdmissionID {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.' || c == ':') {
			return false
		}
	}
	return v.TaskID != "" && v.SessionID != "" && v.Consumer != "" && v.Profile != "" && len(v.TaskID) <= 256 && len(v.SessionID) <= 256 && len(v.Profile) <= 256
}
func custodyLineage(v *base.WorkContextV1) *base.WorkContextV1 {
	c := proto.Clone(v).(*base.WorkContextV1)
	c.Audience, c.KeyId, c.Nonce = "", "", ""
	c.IssuedAtUnix, c.NotBeforeUnix, c.ExpiresAtUnix = 0, 0, 0
	if n := len(c.ActorChain); n > 0 {
		c.ActorChain[n-1].GrantedScopes = nil
	} else {
		c.AuthorityScopes = nil
	}
	return c
}
func custodyScope(p ExecutionConsumerPolicy, lookup bool) []*gen.WorkContextScope {
	actions := []string{p.ReadAction}
	if !lookup {
		actions = []string{p.InvokeAction, p.ReadAction}
	}
	return []*gen.WorkContextScope{{ResourceKind: p.ResourceKind, ResourceIds: []string{p.ResourceID}, Actions: actions}}
}

func (b *executionCustody) register(ctx context.Context, identity *auth.Identity, in wire.RegisterRequest) (wire.Registration, error) {
	var zero wire.Registration
	p, ok := b.config.Consumers[in.Binding.Consumer]
	ri := auth.RequestIdentityOf(identity)
	if !ok || !validCustodyBinding(in.Binding) || in.Binding.TaskClaimsDigest != "" || p.Profile != in.Binding.Profile || in.Binding.OrgID != identity.OrgID.String() || in.Binding.OwnerID != ri.EffectiveSubjectID() {
		return zero, status.Error(codes.PermissionDenied, "")
	}
	ctx = stampRequestIdentity(ctx, ri, identity.Assurance())
	owner, err := b.config.Authority.authorizeOwner(ctx, in.Binding.OrgID)
	if err != nil {
		return zero, err
	}
	parentToken, parent, actor, err := b.config.Authority.verifyParent(ctx, in.Binding.OrgID, owner, in.ParentToken)
	if err != nil {
		return zero, err
	}
	if parent.Audience != p.ParentAudience || parent.TaskId != in.Binding.TaskID || parent.SessionId != in.Binding.SessionID || parent.ReplayPolicy != codefly.WorkContextReplayIdempotent || in.TaskExpiresAt > parent.ExpiresAtUnix || in.TaskExpiresAt <= time.Now().Unix()+3 {
		return zero, status.Error(codes.PermissionDenied, "")
	}
	for _, action := range []string{p.InvokeAction, p.ReadAction} {
		if codefly.RequireWorkContextScope(parent, codefly.WorkContextScopeRequirement{ResourceKind: p.ResourceKind, ResourceID: p.ResourceID, Action: action, RequireExplicitResource: true}) != nil {
			return zero, status.Error(codes.PermissionDenied, "")
		}
	}
	payload := custodyPayload{Request: in, RealActor: ri.RealActorID(), Delegation: ri.Delegation, PolicyDigest: custodyJSONHash(p)}
	// Fingerprint excludes server-generated child/nonce. Exact parent bytes and
	// immutable absolute requested horizon remain part of the registration key.
	fingerprint := custodyJSONHash(payload)
	stored, err := b.config.Store.FindExecutionCustody(ctx, in.Binding.OrgID, owner, in.Binding.AdmissionID)
	if err != nil {
		return zero, status.Error(codes.Unavailable, "")
	}
	if stored.Reference == "" {
		ttl := min(int64(900), in.TaskExpiresAt-time.Now().Unix()-3)
		issued, err := b.config.Authority.exchangeVerifiedParent(parentToken, parent, actor, &gen.ExchangeWorkContextAudienceRequest{OrgId: in.Binding.OrgID, Audience: p.TaskAudience, AttenuatedScopes: []*gen.WorkContextScope{{ResourceKind: p.TaskResourceKind, ResourceIds: []string{parent.TaskId}, Actions: p.TaskActions}}, ReplayPolicy: gen.WorkContextReplayPolicy_WORK_CONTEXT_REPLAY_POLICY_IDEMPOTENT, TtlSeconds: int32(ttl)})
		if err != nil {
			return zero, err
		}
		token, err := codefly.ParseWorkContextToken(issued.Token)
		if err != nil {
			return zero, status.Error(codes.Unavailable, "")
		}
		task, err := b.config.Authority.verifier.Verify(token, codefly.WorkContextExpectations{Issuer: b.config.Authority.issuer, Audience: p.TaskAudience, TenantID: in.Binding.OrgID, OwnerPrincipalID: owner})
		if err != nil || task.ExpiresAtUnix > in.TaskExpiresAt {
			return zero, status.Error(codes.FailedPrecondition, "")
		}
		payload.TaskToken = issued.Token
		payload.ClaimsDigest, err = wire.ClaimsDigest(task)
		if err != nil {
			return zero, status.Error(codes.Unavailable, "")
		}
		raw, _ := json.Marshal(payload)
		record := business.ExecutionCustodyRecord{Reference: uuid.NewString(), OrgID: in.Binding.OrgID, OwnerID: owner, AdmissionID: in.Binding.AdmissionID, Fingerprint: fingerprint, ExpiresAt: time.Unix(task.ExpiresAtUnix, 0)}
		record.Envelope, err = b.config.Cipher.EncryptSecret(ctx, "execution-custody/"+record.Reference, string(raw))
		if err != nil {
			return zero, status.Error(codes.Unavailable, "")
		}
		stored, err = b.config.Store.RegisterExecutionCustody(ctx, record)
		if err != nil {
			return zero, status.Error(codes.Unavailable, "")
		}
	}
	if stored.Fingerprint != fingerprint {
		return zero, status.Error(codes.AlreadyExists, "")
	}
	if stored.Envelope == "" || stored.ExpiresAt.Unix() <= time.Now().Unix()+3 {
		return zero, status.Error(codes.FailedPrecondition, "")
	}
	saved, err := b.open(ctx, stored)
	if err != nil {
		return zero, err
	}
	binding := saved.Request.Binding
	binding.TaskClaimsDigest = saved.ClaimsDigest
	return wire.Registration{Reference: stored.Reference, Binding: binding, ExpiresAt: stored.ExpiresAt.Unix(), TaskToken: saved.TaskToken}, nil
}

func (b *executionCustody) recoverRegistration(ctx context.Context, identity *auth.Identity, in wire.RecoverRequest) (wire.Registration, error) {
	var zero wire.Registration
	ri := auth.RequestIdentityOf(identity)
	if !validCustodyBinding(in.Binding) || in.Binding.TaskClaimsDigest != "" || ri.EffectiveSubjectID() != in.Binding.OwnerID || ri.OrgID.String() != in.Binding.OrgID {
		return zero, status.Error(codes.PermissionDenied, "")
	}
	ctx = stampRequestIdentity(ctx, ri, identity.Assurance())
	owner, err := b.config.Authority.authorizeOwner(ctx, in.Binding.OrgID)
	if err != nil {
		return zero, err
	}
	record, err := b.config.Store.FindExecutionCustody(ctx, in.Binding.OrgID, owner, in.Binding.AdmissionID)
	if err != nil {
		return zero, status.Error(codes.Unavailable, "")
	}
	if record.Reference == "" {
		return zero, status.Error(codes.NotFound, "")
	}
	if record.Envelope == "" || record.ExpiresAt.Unix() <= time.Now().Unix()+3 {
		return zero, status.Error(codes.FailedPrecondition, "")
	}
	payload, err := b.open(ctx, record)
	if err != nil {
		return zero, err
	}
	if payload.Request.Binding != in.Binding || payload.Request.TaskExpiresAt != in.TaskExpiresAt || payload.RealActor != ri.RealActorID() || custodyJSONHash(payload.Delegation) != custodyJSONHash(ri.Delegation) {
		return zero, status.Error(codes.PermissionDenied, "")
	}
	_, _, _, err = b.config.Authority.verifyParent(ctx, record.OrgID, owner, payload.Request.ParentToken)
	if err != nil {
		return zero, err
	}
	binding := payload.Request.Binding
	binding.TaskClaimsDigest = payload.ClaimsDigest
	return wire.Registration{Reference: record.Reference, Binding: binding, ExpiresAt: record.ExpiresAt.Unix(), TaskToken: payload.TaskToken}, nil
}

func (b *executionCustody) open(ctx context.Context, record business.ExecutionCustodyRecord) (custodyPayload, error) {
	var payload custodyPayload
	raw, err := b.config.Cipher.DecryptSecret(ctx, "execution-custody/"+record.Reference, record.Envelope)
	if err != nil {
		return payload, status.Error(codes.Unavailable, "")
	}
	if json.Unmarshal([]byte(raw), &payload) != nil {
		return payload, status.Error(codes.PermissionDenied, "")
	}
	original := payload
	original.TaskToken = ""
	original.ClaimsDigest = ""
	if custodyJSONHash(original) != record.Fingerprint {
		return payload, status.Error(codes.PermissionDenied, "")
	}
	p, ok := b.config.Consumers[payload.Request.Binding.Consumer]
	if !ok || payload.PolicyDigest != custodyJSONHash(p) || payload.Request.Binding.OrgID != record.OrgID || payload.Request.Binding.OwnerID != record.OwnerID || payload.Request.Binding.AdmissionID != record.AdmissionID {
		return payload, status.Error(codes.PermissionDenied, "")
	}
	token, err := codefly.ParseWorkContextToken(payload.TaskToken)
	if err != nil {
		return payload, status.Error(codes.PermissionDenied, "")
	}
	task, err := b.config.Authority.verifier.Verify(token, codefly.WorkContextExpectations{Issuer: b.config.Authority.issuer, Audience: p.TaskAudience, TenantID: record.OrgID, OwnerPrincipalID: record.OwnerID})
	if err != nil {
		return payload, status.Error(codes.PermissionDenied, "")
	}
	digest, err := wire.ClaimsDigest(task)
	if err != nil || digest != payload.ClaimsDigest || task.ExpiresAtUnix != record.ExpiresAt.Unix() || task.ExpiresAtUnix > payload.Request.TaskExpiresAt || task.TaskId != payload.Request.Binding.TaskID || task.SessionId != payload.Request.Binding.SessionID {
		return payload, status.Error(codes.PermissionDenied, "")
	}
	return payload, nil
}

func (b *executionCustody) exchange(ctx context.Context, worker string, in wire.ExchangeRequest) (wire.Child, error) {
	var zero wire.Child
	p, ok := b.config.Consumers[in.Binding.Consumer]
	if !ok || worker != p.WorkerURI || p.Profile != in.Binding.Profile || p.Audience != in.Audience || !validCustodyBinding(in.Binding) {
		return zero, status.Error(codes.PermissionDenied, "")
	}
	if _, err := uuid.Parse(in.Reference); err != nil {
		return zero, status.Error(codes.InvalidArgument, "")
	}
	record, err := b.config.Store.GetExecutionCustody(ctx, in.Reference)
	if err != nil {
		return zero, status.Error(codes.Unavailable, "")
	}
	if record.Reference == "" {
		return zero, status.Error(codes.NotFound, "")
	}
	if record.OrgID != in.Binding.OrgID || record.OwnerID != in.Binding.OwnerID || record.AdmissionID != in.Binding.AdmissionID {
		return zero, status.Error(codes.PermissionDenied, "")
	}
	if record.Envelope == "" || record.ExpiresAt.Unix() <= time.Now().Unix()+3 {
		return zero, status.Error(codes.FailedPrecondition, "")
	}
	payload, err := b.open(ctx, record)
	if err != nil {
		return zero, err
	}
	expected := payload.Request.Binding
	expected.TaskClaimsDigest = payload.ClaimsDigest
	if expected != in.Binding || payload.PolicyDigest != custodyJSONHash(p) {
		return zero, status.Error(codes.PermissionDenied, "")
	}
	// This is a deliberately Accounts-owned delegated broker path, not an owner
	// session. Only verified binding data may establish database scope. Canonical
	// parent verification rechecks membership, every revision and actor revocation.
	ctx = auth.WithVerifiedDatabaseIdentity(ctx, record.OwnerID, record.OrgID)
	parentToken, parent, actor, err := b.config.Authority.verifyParent(ctx, record.OrgID, record.OwnerID, payload.Request.ParentToken)
	if err != nil {
		return zero, err
	}
	horizon := min(parent.ExpiresAtUnix, record.ExpiresAt.Unix())
	ttl := min(int64(900), horizon-time.Now().Unix()-3)
	if ttl <= 0 {
		return zero, status.Error(codes.FailedPrecondition, "")
	}
	req := &gen.ExchangeWorkContextAudienceRequest{OrgId: record.OrgID, Audience: p.Audience, AttenuatedScopes: custodyScope(p, in.Lookup), ReplayPolicy: gen.WorkContextReplayPolicy_WORK_CONTEXT_REPLAY_POLICY_IDEMPOTENT, TtlSeconds: int32(ttl)}
	issued, err := b.config.Authority.exchangeVerifiedParent(parentToken, parent, actor, req)
	if err != nil {
		return zero, err
	}
	childToken, err := codefly.ParseWorkContextToken(issued.Token)
	if err != nil {
		return zero, status.Error(codes.Unavailable, "")
	}
	child, err := b.config.Authority.verifier.Verify(childToken, codefly.WorkContextExpectations{Issuer: b.config.Authority.issuer, Audience: p.Audience, TenantID: record.OrgID, OwnerPrincipalID: record.OwnerID})
	if err != nil || child.ExpiresAtUnix > horizon || child.ExpiresAtUnix <= time.Now().Unix() {
		return zero, status.Error(codes.FailedPrecondition, "")
	}
	return wire.Child{Token: issued.Token, ExpiresAt: child.ExpiresAtUnix}, nil
}
