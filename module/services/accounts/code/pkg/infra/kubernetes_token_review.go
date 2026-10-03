package infra

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"
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

// KubernetesClient is the host's read-only window onto the API server.
type KubernetesClient struct {
	endpoint string
	http     *http.Client
	// token is this SERVICE'S own credential, re-read per request.
	//
	// Re-read rather than cached: a projected service-account token is rotated
	// by the kubelet at ~80% of its lifetime, and a value captured at boot stops
	// working some hours later — which presents as the API server refusing the
	// host's own identity, the single most confusing failure available here.
	tokenPath string
}

// NewKubernetesClient builds the client from the in-cluster environment.
//
// It refuses when the environment is not a cluster, rather than constructing a
// client that will fail on first use. A host configured to verify executions
// that cannot reach the API server must say so at boot, for the same reason the
// keyless trust policy refuses at boot: "I cannot perform this check" and "this
// caller failed the check" must not be the same observable.
func NewKubernetesClient() (*KubernetesClient, error) {
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
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(authority) {
		return nil, fmt.Errorf("%w: the cluster CA at %s contains no usable certificate", ErrKubernetesUnavailable, inClusterCAPath)
	}
	if _, err := os.ReadFile(inClusterTokenPath); err != nil {
		return nil, fmt.Errorf("%w: cannot read this service's own token at %s: %w",
			ErrKubernetesUnavailable, inClusterTokenPath, err)
	}
	return &KubernetesClient{
		endpoint:  "https://" + host + ":" + port,
		tokenPath: inClusterTokenPath,
		http: &http.Client{
			// A bounded timeout, because every caller of this is on a request
			// path that must fail rather than hang. The delivered namespaces are
			// default-deny egress, so a misconfigured NetworkPolicy makes this
			// call hang rather than refuse — and without a deadline that becomes
			// a request the client gave up on while the host still holds its
			// resources.
			Timeout: 10 * time.Second,
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS13},
			},
		},
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
// TokenReview without one validates the token for any audience, which would let
// a token minted for another service — a token its holder was legitimately
// given, for a different purpose — authenticate here. Passing the audience is
// what makes this an authentication of a caller rather than of a bearer.
func (c *KubernetesClient) ReviewToken(ctx context.Context, token, audience string) (*ReviewedToken, error) {
	if strings.TrimSpace(token) == "" {
		return nil, fmt.Errorf("%w: no token was presented", ErrTokenRejected)
	}
	if strings.TrimSpace(audience) == "" {
		// A programming error rather than a caller's: refuse loudly instead of
		// issuing an audience-less review that would accept too much.
		return nil, errors.New("a token review requires an audience; an audience-less review accepts a token minted for any service")
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

// RunningContainerImage is what one container of one pod is actually running.
type RunningContainerImage struct {
	// UID is the pod's metadata.uid, for comparison against the UID the token
	// named. A pod NAME is reused across generations of a workload; the UID is
	// not, which is why the comparison is on the UID.
	UID string
	// ImageID is the running image's digest-bearing reference from the
	// container's STATUS — what it is running — never the spec's image, which
	// is what it was asked to run.
	ImageID string
	// Found reports whether the named container exists in the pod's statuses at
	// all. A missing container is distinct from a container running the wrong
	// thing, and an execution-bound decision must refuse both rather than treat
	// an absence as a non-match it can explain.
	Found bool
}

type podResponse struct {
	Metadata struct {
		UID string `json:"uid"`
	} `json:"metadata"`
	Status struct {
		ContainerStatuses []struct {
			Name    string `json:"name"`
			ImageID string `json:"imageID"`
		} `json:"containerStatuses"`
		InitContainerStatuses []struct {
			Name    string `json:"name"`
			ImageID string `json:"imageID"`
		} `json:"initContainerStatuses"`
	} `json:"status"`
}

// RunningContainer reads one named container's running image from one pod.
//
// Only `get` is used, never `list`: the host asks about a pod it was told about
// by a TokenReview, so it never needs to enumerate. That keeps the RBAC the
// deployment must grant to `get` on pods in the cell's declared namespaces —
// which, stated accurately, reaches any pod in those namespaces by name, because
// `resourceNames` cannot be used when replica pod names are generated. No
// enumeration, and only within those namespaces.
//
// Init container statuses are searched too, so that a workload naming an init
// container as its authenticating one is answered rather than silently reported
// as absent. Whether an init container MAY authenticate is a policy question the
// caller decides from the document's `non_authenticating` list; this is a read.
func (c *KubernetesClient) RunningContainer(
	ctx context.Context, namespace, pod, container string,
) (*RunningContainerImage, error) {
	if namespace == "" || pod == "" || container == "" {
		return nil, fmt.Errorf("%w: a pod read needs a namespace, a pod and a container name", ErrKubernetesUnavailable)
	}
	var body podResponse
	if err := c.get(ctx, "/api/v1/namespaces/"+namespace+"/pods/"+pod, &body); err != nil {
		return nil, err
	}
	image := &RunningContainerImage{UID: body.Metadata.UID}
	for _, status := range body.Status.ContainerStatuses {
		if status.Name == container {
			image.ImageID, image.Found = status.ImageID, true
			return image, nil
		}
	}
	for _, status := range body.Status.InitContainerStatuses {
		if status.Name == container {
			image.ImageID, image.Found = status.ImageID, true
			return image, nil
		}
	}
	return image, nil
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
