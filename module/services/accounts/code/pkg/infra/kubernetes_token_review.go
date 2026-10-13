package infra

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"accounts/pkg/business"
)

// The Kubernetes API the host needs, and nothing more.
//
// This is a direct REST client rather than client-go, deliberately. The host
// needs exactly two calls — review a token, read one pod by name — and client-go
// would bring a dependency tree larger than this service's own in order to
// provide them, along with informers, caches and a watch machinery that a
// verifier must not have. What it would buy is convenience over an API that is
// stable and well-specified; what it would cost is an auditable surface. Two
// request shapes are readable in full; a cache is not.
//
// Everything here is read-only against the API server. There is no code path
// that writes a Kubernetes object.

// ErrKubernetesUnavailable reports that the API server could not be asked.
//
// It is distinct from a refusal on purpose, and the distinction decides a
// response code: a token the API server REFUSED is a terminal 401 (the caller's
// credential is wrong and retrying asks the same question), while an API server
// that could not be REACHED is a retryable 503. Collapsing them would make a
// control-plane outage look like a fleet of misconfigured callers.
var ErrKubernetesUnavailable = errors.New("kubernetes api is unavailable")

// ErrTokenRejected reports that the API server reviewed the token and said no.
var ErrTokenRejected = errors.New("kubernetes rejected the presented token")

// The in-cluster service account paths. Fixed by Kubernetes, not configurable:
// a configurable path is a way to point the host's own identity somewhere else.
const (
	inClusterTokenPath     = "/var/run/secrets/kubernetes.io/serviceaccount/token"
	inClusterCAPath        = "/var/run/secrets/kubernetes.io/serviceaccount/ca.crt"
	inClusterNamespacePath = "/var/run/secrets/kubernetes.io/serviceaccount/namespace"
)

// InCluster reports whether this process carries any of what the kubelet puts
// in a pod: the API server's address in KUBERNETES_SERVICE_HOST, or the
// projected service-account token or CA bundle at their fixed paths. Any one of
// them is enough to answer yes, and a path that cannot be checked counts as
// present.
//
// It is the host's answer to "is this a pod", read from the same facts
// NewKubernetesClient builds its identity from, and it errs toward yes on
// purpose: callers use it to REFUSE a local-only setting.
//
// What defeating it takes, precisely. The kubelet injects
// KUBERNETES_SERVICE_HOST into every container, and no configuration value can
// set or clear it: workspace values arrive under their own prefixed carriers.
// The projected files are present only when the pod mounts a service-account
// token, and a Codefly-rendered service pod does not by default
// (automountServiceAccountToken: false). So in a default pod the variable is the
// only fact, and an explicit container-env override in the pod spec clears it.
// That takes control of the pod spec, not of a configuration value.
func InCluster() bool {
	return inCluster(os.Getenv("KUBERNETES_SERVICE_HOST"), inClusterProjectedPaths...)
}

// inClusterProjectedPaths are the kubelet's fixed paths InCluster checks. A
// variable only so a test can point it at files it controls; nothing assigns
// it outside a test.
var inClusterProjectedPaths = []string{inClusterTokenPath, inClusterCAPath}

// inCluster is InCluster over facts it has already located, so a test drives
// each branch through the same code rather than a lookalike.
func inCluster(serviceHost string, projected ...string) bool {
	if strings.TrimSpace(serviceHost) != "" {
		return true
	}
	for _, path := range projected {
		if _, err := os.Stat(path); err == nil || !errors.Is(err, fs.ErrNotExist) {
			return true
		}
	}
	return false
}

// KubernetesClient is the host's read-only window onto the API server.
type KubernetesClient struct {
	nativeDevelopment bool // set only by the non-pod stand-in constructor
	endpoint          string
	http              *http.Client
	// token is this SERVICE'S own credential, re-read per request.
	//
	// Re-read rather than cached: a projected service-account token is rotated
	// by the kubelet at ~80% of its lifetime, and a value captured at boot stops
	// working some hours later — which presents as the API server refusing the
	// host's own identity, the single most confusing failure available here.
	tokenPath string
	// authority is the cluster CA bundle the kubelet projected, as read at
	// construction: the roots this client trusts, and what a local verification
	// policy's cluster binding is checked against (BindToAuthority).
	authority []byte
	// namespacePath is the kubelet-projected namespace file, read when this
	// host reviews its OWN token to prove which cluster it runs in. Fixed in
	// production (inClusterNamespacePath); a field only so a test can supply
	// one.
	namespacePath string
}

// errKubernetesRedirect refuses every redirect the API server answers with.
var errKubernetesRedirect = errors.New("the kubernetes api answered with a redirect, and this host follows none")

// newKubernetesHTTPClient is the one transport every call to the API server
// uses: TLS 1.3 against exactly the given roots, a bounded timeout, and NO
// redirects.
//
// Redirects are refused because a followed redirect is a request to a server
// nobody configured: a TokenReview POSTed to wherever a response pointed is a
// credential decision made by that server. The API server answers the calls
// this host makes directly; a redirect is a misconfiguration or an attack, and
// either way it is refused rather than followed.
func newKubernetesHTTPClient(roots *x509.CertPool) *http.Client {
	return &http.Client{
		// A bounded timeout, because every caller of this is on a request
		// path that must fail rather than hang. The delivered namespaces are
		// default-deny egress, so a misconfigured NetworkPolicy makes this
		// call hang rather than refuse — and without a deadline that becomes
		// a request the client gave up on while the host still holds its
		// resources.
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS13},
		},
		CheckRedirect: func(*http.Request, []*http.Request) error { return errKubernetesRedirect },
	}
}

// NewKubernetesClient builds the client from the in-cluster environment.
//
// It refuses when the environment is not a cluster, rather than constructing a
// client that will fail on first use. A host configured to verify executions
// that cannot reach the API server must say so at boot, for the same reason the
// pinned-key trust policy refuses at boot: "I cannot perform this check" and "this
// caller failed the check" must not be the same observable.
func NewKubernetesClient() (*KubernetesClient, error) {
	if dir := strings.TrimSpace(os.Getenv(LocalClusterEnvironment)); dir != "" {
		return localClusterClient(dir, InCluster())
	}
	host := strings.TrimSpace(os.Getenv("KUBERNETES_SERVICE_HOST"))
	port := strings.TrimSpace(os.Getenv("KUBERNETES_SERVICE_PORT"))
	if host == "" || port == "" {
		return nil, fmt.Errorf("%w: KUBERNETES_SERVICE_HOST/PORT are unset, so this is not an in-cluster environment",
			ErrKubernetesUnavailable)
	}
	authority, err := os.ReadFile(inClusterCAPath)
	if err != nil {
		return nil, fmt.Errorf("%w: cannot read the cluster CA at %s: %w", ErrKubernetesUnavailable, inClusterCAPath, err)
	}
	return kubernetesClientFrom("https://"+host+":"+port, authority, inClusterTokenPath, inClusterNamespacePath)
}

// LocalClusterEnvironment names a directory that stands in for the kubelet's
// projection when this host is NOT a pod: a developer's run of the composition
// on a laptop, where the run itself is the cluster — it serves the TokenReview
// and pod reads, issues each workload its projected token, and writes into this
// directory what a kubelet would project into the host's pod:
//
//	server     the API server's https origin
//	ca.crt     the one certificate authority that API server serves under
//	token      this service's own token
//	namespace  the namespace this service runs in
//
// The in-cluster paths stay fixed and unconfigurable, for the reason given
// above them, and this does not change that: IN A POD THIS VARIABLE IS REFUSED,
// not ignored. A pod's identity is the kubelet's to project, so a pod that
// names a stand-in is a pod whose identity somebody is trying to point
// elsewhere, and the host says so at boot rather than serving. Outside a pod
// there is no kubelet and nothing to redirect: the stand-in is the only cluster
// there is, and the host's local trust profile is still proven against it
// (BindToAuthority) before a single delivery is admitted.
const LocalClusterEnvironment = "SOLUTION_HOST_LOCAL_CLUSTER"

// ErrLocalClusterInAPod refuses a stand-in named inside a real cluster.
var ErrLocalClusterInAPod = errors.New("a pod may not name a local stand-in for its cluster")

// localClusterClient builds the client from a stand-in directory. inPod is
// InCluster's answer, passed in so a test drives the refusal through this code.
func localClusterClient(dir string, inPod bool) (*KubernetesClient, error) {
	if inPod {
		return nil, fmt.Errorf("%w: %s is set and this process carries what a kubelet puts in a pod; "+
			"a pod reads its identity from the kubelet's projection and from nowhere else",
			ErrLocalClusterInAPod, LocalClusterEnvironment)
	}
	if !filepath.IsAbs(dir) {
		return nil, fmt.Errorf("%w: %s must be an absolute directory, got %q", ErrKubernetesUnavailable, LocalClusterEnvironment, dir)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "server"))
	if err != nil {
		return nil, fmt.Errorf("%w: cannot read the local cluster's API server address: %w", ErrKubernetesUnavailable, err)
	}
	server, err := url.Parse(strings.TrimSpace(string(raw)))
	if err != nil || server.Scheme != "https" || server.Host == "" || server.Port() == "" || server.User != nil ||
		(server.Path != "" && server.Path != "/") || server.RawQuery != "" || server.Fragment != "" {
		return nil, fmt.Errorf("%w: the local cluster's API server address must be an https origin and nothing more", ErrKubernetesUnavailable)
	}
	authority, err := os.ReadFile(filepath.Join(dir, "ca.crt"))
	if err != nil {
		return nil, fmt.Errorf("%w: cannot read the local cluster's CA: %w", ErrKubernetesUnavailable, err)
	}
	client, err := kubernetesClientFrom("https://"+server.Host, authority, filepath.Join(dir, "token"), filepath.Join(dir, "namespace"))
	if err != nil {
		return nil, err
	}
	client.nativeDevelopment = true
	return client, nil
}

// kubernetesClientFrom is the construction NewKubernetesClient performs, over
// pieces it has already located — separated so a test builds its client through
// the same code (the same roots, the same redirect refusal) rather than a
// lookalike.
func kubernetesClientFrom(endpoint string, authority []byte, tokenPath, namespacePath string) (*KubernetesClient, error) {
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(authority) {
		return nil, fmt.Errorf("%w: the cluster CA bundle contains no usable certificate", ErrKubernetesUnavailable)
	}
	if _, err := os.ReadFile(tokenPath); err != nil {
		return nil, fmt.Errorf("%w: cannot read this service's own token at %s: %w",
			ErrKubernetesUnavailable, tokenPath, err)
	}
	return &KubernetesClient{
		endpoint:      endpoint,
		tokenPath:     tokenPath,
		authority:     authority,
		namespacePath: namespacePath,
		http:          newKubernetesHTTPClient(pool),
	}, nil
}

// InClusterNamespace is the namespace this service runs in.
func InClusterNamespace() (string, error) {
	namespace, err := os.ReadFile(inClusterNamespacePath)
	if err != nil {
		return "", fmt.Errorf("%w: cannot read this service's namespace: %w", ErrKubernetesUnavailable, err)
	}
	return strings.TrimSpace(string(namespace)), nil
}

// ReviewedToken is what a TokenReview established about a caller.
type ReviewedToken struct {
	// ServiceAccount and Namespace are parsed out of the authenticated
	// username, which Kubernetes renders as
	// `system:serviceaccount:<namespace>:<name>`.
	ServiceAccount string
	Namespace      string
	// PodName and PodUID come from the authentication extra claims a BOUND
	// token carries. Empty for a legacy non-bound token, which is a meaningful
	// absence rather than a blank: a credential that names no pod cannot be
	// checked against a pod, so an execution-bound decision must refuse it
	// rather than skip the check.
	PodName string
	PodUID  string
}

// tokenReviewRequest / tokenReviewResponse are the minimum of Kubernetes'
// TokenReview that the host uses. Written out rather than imported so the wire
// shape this depends on is visible in one place.
type tokenReviewRequest struct {
	APIVersion string                 `json:"apiVersion"`
	Kind       string                 `json:"kind"`
	Spec       tokenReviewRequestSpec `json:"spec"`
}

type tokenReviewRequestSpec struct {
	Token     string   `json:"token"`
	Audiences []string `json:"audiences,omitempty"`
}

type tokenReviewResponse struct {
	Status struct {
		Authenticated bool     `json:"authenticated"`
		Audiences     []string `json:"audiences"`
		Error         string   `json:"error"`
		User          struct {
			Username string              `json:"username"`
			Extra    map[string][]string `json:"extra"`
		} `json:"user"`
	} `json:"status"`
}

// Extra claims a bound service-account token carries, naming the pod it was
// projected into.
const (
	podNameClaim = "authentication.kubernetes.io/pod-name"
	podUIDClaim  = "authentication.kubernetes.io/pod-uid"
)

// ReviewToken asks the API server whether a token is valid FOR THIS AUDIENCE,
// and what it authenticates as.
//
// The audience is required and is checked by the API server, not here. A
// TokenReview without one is checked against the API server's OWN audiences,
// so the token every pod is given for talking to the API server — a token its
// holder was legitimately given, for a different purpose — would authenticate
// here. Passing the audience is what makes this an authentication of a caller
// for THIS service rather than of a bearer of any cluster credential.
func (c *KubernetesClient) reviewToken(ctx context.Context, token, audience string) (*ReviewedToken, error) {
	if strings.TrimSpace(token) == "" {
		return nil, fmt.Errorf("%w: no token was presented", ErrTokenRejected)
	}
	if strings.TrimSpace(audience) == "" {
		// A programming error rather than a caller's: refuse loudly instead of
		// issuing an audience-less review that would accept too much.
		return nil, errors.New("a token review requires an audience; an audience-less review accepts a token minted for the api server itself")
	}
	body, err := json.Marshal(tokenReviewRequest{
		APIVersion: "authentication.k8s.io/v1",
		Kind:       "TokenReview",
		Spec:       tokenReviewRequestSpec{Token: token, Audiences: []string{audience}},
	})
	if err != nil {
		return nil, err
	}

	var reviewed tokenReviewResponse
	if err := c.post(ctx, "/apis/authentication.k8s.io/v1/tokenreviews", body, &reviewed); err != nil {
		return nil, err
	}
	if !reviewed.Status.Authenticated {
		reason := reviewed.Status.Error
		if reason == "" {
			reason = "the api server did not say why"
		}
		return nil, fmt.Errorf("%w: %s", ErrTokenRejected, reason)
	}
	// The API server echoes the audiences the token is valid for. Requiring our
	// audience to be among them is belt and braces over the request above — but
	// it is cheap, and it is the check that would catch an API server version
	// that ignored the request's audiences rather than refusing.
	if !containsString(reviewed.Status.Audiences, audience) {
		return nil, fmt.Errorf("%w: the token is not valid for audience %q", ErrTokenRejected, audience)
	}

	account, namespace, err := parseServiceAccountUsername(reviewed.Status.User.Username)
	if err != nil {
		return nil, err
	}
	return &ReviewedToken{
		ServiceAccount: account,
		Namespace:      namespace,
		PodName:        firstClaim(reviewed.Status.User.Extra, podNameClaim),
		PodUID:         firstClaim(reviewed.Status.User.Extra, podUIDClaim),
	}, nil
}

// authenticatedTokenLifetime reads iat and exp out of a ServiceAccount JWT the
// API server has ALREADY authenticated. It verifies nothing itself — the
// TokenReview did — and so must never be called on a token that has not been
// reviewed. A token whose lifetime cannot be read is refused rather than given
// an assumed one.
func authenticatedTokenLifetime(token string) (time.Time, time.Time, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return time.Time{}, time.Time{}, fmt.Errorf("%w: the authenticated token is not a JWT, so its lifetime cannot be read", ErrTokenRejected)
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("%w: the authenticated token's claims do not decode", ErrTokenRejected)
	}
	var claims struct {
		IssuedAt  *int64 `json:"iat"`
		ExpiresAt *int64 `json:"exp"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil || claims.IssuedAt == nil || claims.ExpiresAt == nil {
		return time.Time{}, time.Time{}, fmt.Errorf("%w: the authenticated token carries no iat and exp", ErrTokenRejected)
	}
	return time.Unix(*claims.IssuedAt, 0).UTC(), time.Unix(*claims.ExpiresAt, 0).UTC(), nil
}

// podResponse is the minimum of a Pod the host reads: its identity and
// lifecycle, and every container — regular, init and ephemeral — with the
// spec's image and the status's live state and imageID.
type podResponse struct {
	Metadata struct {
		UID               string  `json:"uid"`
		DeletionTimestamp *string `json:"deletionTimestamp"`
	} `json:"metadata"`
	// Spec carries what the pod ASKED to run. Read so the declared image is a
	// real value this host holds — reported, never compared against an
	// approval — and so every declared container is accounted for even before
	// its status appears.
	Spec struct {
		Containers          []specContainer `json:"containers"`
		InitContainers      []specContainer `json:"initContainers"`
		EphemeralContainers []specContainer `json:"ephemeralContainers"`
	} `json:"spec"`
	Status struct {
		Phase                      string            `json:"phase"`
		ContainerStatuses          []containerStatus `json:"containerStatuses"`
		InitContainerStatuses      []containerStatus `json:"initContainerStatuses"`
		EphemeralContainerStatuses []containerStatus `json:"ephemeralContainerStatuses"`
	} `json:"status"`
}

type specContainer struct {
	Name          string `json:"name"`
	Image         string `json:"image"`
	RestartPolicy string `json:"restartPolicy"`
}

type containerStatus struct {
	Name    string `json:"name"`
	ImageID string `json:"imageID"`
	State   struct {
		Running    *struct{} `json:"running"`
		Waiting    *struct{} `json:"waiting"`
		Terminated *struct{} `json:"terminated"`
	} `json:"state"`
}

func (status containerStatus) state() business.ContainerState {
	switch {
	case status.State.Running != nil:
		return business.ContainerRunning
	case status.State.Terminated != nil:
		return business.ContainerTerminated
	case status.State.Waiting != nil:
		return business.ContainerWaiting
	}
	return business.ContainerStateUnknown
}

// readPod reads one pod: its UID, phase, deletion, and every container with
// its kind, live state and running image.
//
// Only `get` is used, never `list`: the host asks about a pod it was told about
// by a TokenReview, so it never needs to enumerate. That keeps the RBAC the
// deployment must grant to `get` on pods in the cell's declared namespaces —
// which, stated accurately, reaches any pod in those namespaces by name, because
// `resourceNames` cannot be used when replica pod names are generated.
//
// EVERY container is returned, from the spec, joined to its status by name. A
// container the spec declares and the status does not yet report is returned
// with state unknown rather than dropped: dropping it would let a container the
// host cannot see vouch for nothing and run anyway.
func (c *KubernetesClient) readPod(ctx context.Context, namespace, pod string) (*business.PodExecution, error) {
	if namespace == "" || pod == "" {
		return nil, fmt.Errorf("%w: a pod read needs a namespace and a pod name", ErrKubernetesUnavailable)
	}
	var body podResponse
	if err := c.get(ctx, "/api/v1/namespaces/"+namespace+"/pods/"+pod, &body); err != nil {
		return nil, err
	}
	read := &business.PodExecution{
		UID:         body.Metadata.UID,
		Phase:       body.Status.Phase,
		Terminating: body.Metadata.DeletionTimestamp != nil,
	}
	for _, group := range []struct {
		kind     business.ContainerKind
		spec     []specContainer
		statuses []containerStatus
	}{
		{business.ContainerRegular, body.Spec.Containers, body.Status.ContainerStatuses},
		{business.ContainerInit, body.Spec.InitContainers, body.Status.InitContainerStatuses},
		{business.ContainerEphemeral, body.Spec.EphemeralContainers, body.Status.EphemeralContainerStatuses},
	} {
		seen := map[string]bool{}
		for _, declared := range group.spec {
			container := business.ContainerExecution{
				Name: declared.Name, Kind: group.kind, State: business.ContainerStateUnknown,
				DeclaredImage: declared.Image,
				Restartable:   group.kind == business.ContainerInit && declared.RestartPolicy == "Always",
			}
			for _, status := range group.statuses {
				if status.Name == declared.Name {
					container.State, container.ImageID = status.state(), status.ImageID
				}
			}
			seen[declared.Name] = true
			read.Containers = append(read.Containers, container)
		}
		// A status for a container the spec does not name is still a container
		// in this pod; it is reported, never ignored.
		for _, status := range group.statuses {
			if !seen[status.Name] {
				read.Containers = append(read.Containers, business.ContainerExecution{
					Name: status.Name, Kind: group.kind, State: status.state(), ImageID: status.ImageID,
				})
			}
		}
	}
	return read, nil
}

func (c *KubernetesClient) post(ctx context.Context, path string, body []byte, into any) error {
	return c.do(ctx, http.MethodPost, path, body, into)
}

func (c *KubernetesClient) get(ctx context.Context, path string, into any) error {
	return c.do(ctx, http.MethodGet, path, nil, into)
}

// do performs one API call, and maps every failure that is not a decision onto
// ErrKubernetesUnavailable.
//
// That mapping is the important part. A transport failure, a 5xx, a 403 on the
// host's own RBAC and an unparseable body are all "I could not ask" — they say
// nothing about the caller being verified, and a decision inferred from any of
// them would be a verification the host did not perform. The host's own
// credential being refused is especially worth keeping on this side: it is the
// deployment's fault, and reporting it as the caller's would send an operator to
// the wrong service.
func (c *KubernetesClient) do(ctx context.Context, method, path string, body []byte, into any) error {
	// Re-read per request: a projected token is rotated at ~80% of its
	// lifetime, so a value captured at boot stops working some hours later.
	token, err := os.ReadFile(c.tokenPath)
	if err != nil {
		return fmt.Errorf("%w: cannot read this service's own token: %w", ErrKubernetesUnavailable, err)
	}
	var reader *bytes.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	} else {
		reader = bytes.NewReader(nil)
	}
	request, err := http.NewRequestWithContext(ctx, method, c.endpoint+path, reader)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrKubernetesUnavailable, err)
	}
	request.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(token)))
	request.Header.Set("Accept", "application/json")
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}

	response, err := c.http.Do(request)
	if err != nil {
		return fmt.Errorf("%w: %s %s: %w", ErrKubernetesUnavailable, method, path, err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("%w: %s %s answered %d", ErrKubernetesUnavailable, method, path, response.StatusCode)
	}
	if err := json.NewDecoder(response.Body).Decode(into); err != nil {
		return fmt.Errorf("%w: %s %s returned an unreadable body: %w", ErrKubernetesUnavailable, method, path, err)
	}
	return nil
}

// parseServiceAccountUsername splits `system:serviceaccount:<namespace>:<name>`.
//
// A username in any other shape is refused rather than accepted with empty
// parts. Kubernetes authenticates humans and other identities too, and a token
// belonging to one of those must not read as a service account in the ""
// namespace — which would then be compared against a configured namespace and,
// for the presence half where the namespace comes from the document, could
// compare equal to nothing by accident.
func parseServiceAccountUsername(username string) (account, namespace string, err error) {
	const prefix = "system:serviceaccount:"
	if !strings.HasPrefix(username, prefix) {
		return "", "", fmt.Errorf("%w: %q is not a service account identity", ErrTokenRejected, username)
	}
	namespace, account, found := strings.Cut(strings.TrimPrefix(username, prefix), ":")
	if !found || namespace == "" || account == "" {
		return "", "", fmt.Errorf("%w: %q is not a well-formed service account identity", ErrTokenRejected, username)
	}
	return account, namespace, nil
}

func firstClaim(extra map[string][]string, key string) string {
	if values := extra[key]; len(values) > 0 {
		return values[0]
	}
	return ""
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
