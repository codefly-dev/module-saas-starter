package main

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/codefly-dev/core/standards"
	codefly "github.com/codefly-dev/sdk-go"

	authv3 "github.com/envoyproxy/go-control-plane/envoy/service/auth/v3"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/reflection"
	"google.golang.org/protobuf/types/known/emptypb"

	apigen "auth-sidecar/external/saas-starter/accounts"
)

func main() {
	// Envoy config generation mode — produces envoy.yaml and exits.
	// Triggered by GENERATE_ENVOY_CONFIG env var or --generate-envoy flag.
	if shouldGenerateEnvoy() {
		generateEnvoyAndExit()
		return
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	_, err := codefly.Init(ctx)
	if err != nil {
		panic(fmt.Sprintf("codefly init failed: %v", err))
	}

	grpcPort := codefly.For(ctx).WithDefaultNetwork().API(standards.GRPC).NetworkInstance().Port
	var httpPort uint16
	publicOrigin := ""
	httpNet, httpNetErr := codefly.For(ctx).API(standards.REST).ResolveNetworkInstance()
	if httpNetErr != nil || httpNet == nil {
		panic(fmt.Sprintf("Codefly REST endpoint is unavailable: %v", httpNetErr))
	}
	httpPort = httpNet.Port
	publicOrigin, err = canonicalPublicOrigin(httpNet.Address)
	if err != nil {
		panic(fmt.Sprintf("invalid Codefly REST endpoint address: %v", err))
	}

	apiNet := codefly.For(ctx).Service("accounts").Endpoint("grpc").API("grpc").NetworkInstance()
	if apiNet == nil {
		panic("api gRPC endpoint not available")
	}
	apiAddr := fmt.Sprintf("%s:%d", apiNet.Hostname, apiNet.Port)
	// Upstream HTTP URLs used by the gateway listener.
	apiRestNet := codefly.For(ctx).Service("accounts").API("rest").NetworkInstance()
	apiHTTPURL := ""
	internalAPIAddr := ""
	if apiRestNet != nil {
		apiHTTPURL = fmt.Sprintf("http://%s:%d", apiRestNet.Hostname, apiRestNet.Port)
		internalAPIAddr = fmt.Sprintf("%s:%d", apiRestNet.Hostname, apiRestNet.Port)
	}
	if internalAPIAddr == "" {
		panic("api private REST/internal-gRPC endpoint not available")
	}
	apiConnectNet := codefly.For(ctx).Service("accounts").API("connect").NetworkInstance()
	apiConnectURL := ""
	if apiConnectNet != nil {
		apiConnectURL = fmt.Sprintf("http://%s:%d", apiConnectNet.Hostname, apiConnectNet.Port)
	}
	frontendNet := codefly.For(ctx).Service("frontend").API("http").NetworkInstance()
	frontendURL := ""
	if frontendNet != nil {
		frontendURL = fmt.Sprintf("http://%s:%d", frontendNet.Hostname, frontendNet.Port)
	}

	apiConn, err := grpc.NewClient(apiAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		panic(fmt.Sprintf("cannot connect to api at %s: %v", apiAddr, err))
	}
	defer apiConn.Close()
	internalAPIConn, err := grpc.NewClient(internalAPIAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		panic(fmt.Sprintf("cannot connect to internal api at %s: %v", internalAPIAddr, err))
	}
	defer internalAPIConn.Close()

	publicKey := fetchPublicKey(ctx, apiConn)

	sidecar := NewSidecar(internalAPIConn, publicKey)

	grpcServer := grpc.NewServer()
	authv3.RegisterAuthorizationServer(grpcServer, sidecar)
	reflection.Register(grpcServer)

	grpcLis, err := net.Listen("tcp", fmt.Sprintf(":%d", grpcPort))
	if err != nil {
		panic(fmt.Sprintf("failed to listen on grpc port: %v", err))
	}

	fmt.Printf("auth-sidecar gRPC (ext_authz) listening on :%d (api: %s, jwt: %v)\n",
		grpcPort, apiAddr, publicKey != nil)

	// Load generated descriptor REST routes plus explicit YAML extensions.
	routingDir := DefaultRoutingDir()
	restEntries, err := LoadAllRESTRoutes(ctx, routingDir)
	if err != nil {
		panic(fmt.Sprintf("failed to load routes from %s: %v", routingDir, err))
	}
	restEntries = append(restEntries,
		&RouteEntry{Service: "self", Method: http.MethodGet, Path: "/health"},
		&RouteEntry{Service: "self", Method: http.MethodGet, Path: "/healthz"},
		&RouteEntry{Service: "self", Method: http.MethodGet, Path: "/ready"},
	)
	// Load descriptor- and policy-derived Connect paths from the checked,
	// generated gateway route catalog.
	connectEntries, err := LoadConnectRoutesFromCatalog()
	if err != nil {
		panic(fmt.Sprintf("failed to load generated Connect routes: %v", err))
	}
	matcher := NewRouteMatcher(restEntries, connectEntries)
	routeEntries := append(restEntries, connectEntries...)

	// HTTP gateway listener — the single public ingress in dev.
	var httpServer *http.Server
	var rateLimiter *RateLimiter
	if httpPort > 0 {
		// Build upstream map from codefly network mappings.
		// REST and Connect live on different ports of the api binary, so they
		// get distinct upstream keys matching the Service field on RouteEntry.
		upstreams := make(map[string]*url.URL)
		if apiHTTPURL != "" {
			upstreams["accounts"] = MustURL(apiHTTPURL)
		}
		if apiConnectURL != "" {
			upstreams["accounts_connect"] = MustURL(apiConnectURL)
		}
		if frontendURL != "" {
			upstreams["frontend"] = MustURL(frontendURL)
		}
		redisURL, redisErr := codefly.For(ctx).Service("cache").Secret("redis", "connection")
		if redisErr != nil || redisURL == "" {
			redisURL = os.Getenv("REDIS_URL")
		}

		authenticationAttemptLimit, err := configuredAuthenticationAttemptLimit()
		if err != nil {
			panic(err)
		}
		rateLimiter = NewRateLimiter(1000,
			WithRedisURL(redisURL),
			WithAuthenticationAttemptLimit(authenticationAttemptLimit),
		) // 1000 req/min per org/IP; stricter MFA budget is configured separately.
		gateway := NewGateway(sidecar, matcher, upstreams, rateLimiter, WithPublicOrigin(publicOrigin))
		httpServer = &http.Server{
			Addr:    fmt.Sprintf(":%d", httpPort),
			Handler: gateway,
		}
		go func() {
			fmt.Printf("auth-sidecar gateway (HTTP) listening on :%d (api: %s, frontend: %s, routes: %d)\n",
				httpPort, apiHTTPURL, frontendURL, len(routeEntries))
			if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				fmt.Fprintf(os.Stderr, "gateway http server error: %v\n", err)
			}
		}()
	}

	go func() {
		<-ctx.Done()
		log.Println("shutdown: signal received, draining connections...")

		// 1. Stop accepting new HTTP connections and drain existing ones (30s).
		if httpServer != nil {
			log.Println("shutdown: draining HTTP gateway connections (30s timeout)...")
			drainCtx, drainCancel := context.WithTimeout(context.Background(), 30*time.Second)
			if err := httpServer.Shutdown(drainCtx); err != nil {
				log.Printf("shutdown: HTTP gateway drain error: %v", err)
			} else {
				log.Println("shutdown: HTTP gateway drained")
			}
			drainCancel()
		}

		// 2. Gracefully stop gRPC server (finishes in-flight RPCs).
		log.Println("shutdown: stopping gRPC server...")
		grpcServer.GracefulStop()
		log.Println("shutdown: gRPC server stopped")

		// 3. Stop rate limiter cleanup goroutine and close Redis if open.
		if rateLimiter != nil {
			log.Println("shutdown: stopping rate limiter...")
			rateLimiter.Stop()
			log.Println("shutdown: rate limiter stopped")
		}

		log.Println("shutdown: complete")
	}()

	if err := grpcServer.Serve(grpcLis); err != nil {
		fmt.Fprintf(os.Stderr, "grpc server error: %v\n", err)
	}
}

func canonicalPublicOrigin(candidate string) (string, error) {
	candidate = strings.TrimSpace(candidate)
	parsed, err := url.Parse(candidate)
	if err != nil || !parsed.IsAbs() || parsed.Host == "" {
		return "", fmt.Errorf("expected an absolute URL, got %q", candidate)
	}
	if parsed.User != nil || parsed.Opaque != "" || parsed.RawPath != "" ||
		(parsed.Path != "" && parsed.Path != "/") || parsed.RawQuery != "" ||
		parsed.ForceQuery || parsed.Fragment != "" {
		return "", fmt.Errorf("expected an exact origin, got %q", candidate)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", fmt.Errorf("expected an HTTP(S) origin, got %q", candidate)
	}
	return strings.TrimSuffix(candidate, "/"), nil
}

func configuredAuthenticationAttemptLimit() (int, error) {
	raw := strings.TrimSpace(workspaceEnv("security", "MFA_COMPLETION_RATE_LIMIT_PER_MINUTE"))
	if raw == "" {
		return authenticationAttemptLimitPerMinute, nil
	}
	limit, err := strconv.Atoi(raw)
	if err != nil || limit < 1 || limit > 1000 {
		return 0, fmt.Errorf("MFA_COMPLETION_RATE_LIMIT_PER_MINUTE must be an integer between 1 and 1000")
	}
	return limit, nil
}

// fetchPublicKey calls api's GetJWKS to get the Ed25519 public key.
func fetchPublicKey(ctx context.Context, conn *grpc.ClientConn) ed25519.PublicKey {
	client := apigen.NewAuthServiceClient(conn)

	// Retry — api may still be starting
	var resp *apigen.JWKSResponse
	var err error
	for i := 0; i < 30; i++ {
		resp, err = client.GetJWKS(ctx, &emptypb.Empty{})
		if err == nil {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if err != nil {
		log.Printf("WARNING: cannot fetch JWKS from backend: %v (JWT validation disabled)", err)
		return nil
	}

	var jwks struct {
		Keys []struct {
			Kty string `json:"kty"`
			Crv string `json:"crv"`
			X   string `json:"x"`
			Alg string `json:"alg"`
		} `json:"keys"`
	}
	if err := json.Unmarshal([]byte(resp.KeysJson), &jwks); err != nil {
		log.Printf("WARNING: cannot parse JWKS: %v (JWT validation disabled)", err)
		return nil
	}

	for _, key := range jwks.Keys {
		if key.Kty == "OKP" && key.Crv == "Ed25519" && key.Alg == "EdDSA" {
			pubBytes, err := base64.RawURLEncoding.DecodeString(key.X)
			if err != nil {
				log.Printf("WARNING: cannot decode public key: %v", err)
				return nil
			}
			log.Printf("JWT public key loaded from backend JWKS")
			return ed25519.PublicKey(pubBytes)
		}
	}

	log.Printf("WARNING: no Ed25519 key found in JWKS (JWT validation disabled)")
	return nil
}

// shouldGenerateEnvoy returns true if the binary should generate Envoy config
// instead of starting the server.
func shouldGenerateEnvoy() bool {
	if os.Getenv("GENERATE_ENVOY_CONFIG") != "" {
		return true
	}
	for _, arg := range os.Args[1:] {
		if arg == "--generate-envoy" {
			return true
		}
	}
	return false
}

// generateEnvoyAndExit reads generated routes plus explicit extensions, builds
// an Envoy config, and writes it to stdout (or ENVOY_CONFIG_OUTPUT).
func generateEnvoyAndExit() {
	routingDir := DefaultRoutingDir()
	entries, err := LoadAllRESTRoutes(context.Background(), routingDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error loading routes from %s: %v\n", routingDir, err)
		os.Exit(1)
	}
	connectEntries, err := LoadConnectRoutesFromCatalog()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error loading generated Connect routes: %v\n", err)
		os.Exit(1)
	}
	entries = append(entries, connectEntries...)

	// Parse upstream definitions from environment variables.
	// Format: ENVOY_UPSTREAM_<SERVICE>=<host>:<port>
	// Example: ENVOY_UPSTREAM_API=api:5962
	upstreams := parseUpstreamsFromEnv("ENVOY_UPSTREAM_")
	connectUpstreams := parseUpstreamsFromEnv("ENVOY_CONNECT_UPSTREAM_")

	// ext_authz cluster — the sidecar itself.
	extAuthz := Upstream{Address: "127.0.0.1", Port: 9000}
	if v := os.Getenv("ENVOY_EXT_AUTHZ"); v != "" {
		u, parseErr := parseHostPort(v)
		if parseErr != nil {
			fmt.Fprintf(os.Stderr, "error parsing ENVOY_EXT_AUTHZ=%q: %v\n", v, parseErr)
			os.Exit(1)
		}
		extAuthz = u
	}

	listenPort := uint32(8080)
	if v := os.Getenv("ENVOY_LISTEN_PORT"); v != "" {
		p, parseErr := strconv.ParseUint(v, 10, 32)
		if parseErr != nil {
			fmt.Fprintf(os.Stderr, "error parsing ENVOY_LISTEN_PORT=%q: %v\n", v, parseErr)
			os.Exit(1)
		}
		listenPort = uint32(p)
	}

	// Default upstreams if none provided (sensible defaults for the module).
	if len(upstreams) == 0 {
		upstreams = map[string]Upstream{
			"accounts": {Address: "accounts", Port: 5962},
			"frontend": {Address: "frontend", Port: 3000},
		}
	}

	yamlBytes, err := GenerateEnvoyConfig(ToEnvoyConfig(entries), upstreams, connectUpstreams, extAuthz, listenPort)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error generating envoy config: %v\n", err)
		os.Exit(1)
	}

	output := os.Getenv("ENVOY_CONFIG_OUTPUT")
	if output != "" {
		if writeErr := os.WriteFile(output, yamlBytes, 0644); writeErr != nil {
			fmt.Fprintf(os.Stderr, "error writing to %s: %v\n", output, writeErr)
			os.Exit(1)
		}
		fmt.Printf("envoy config written to %s (%d routes)\n", output, len(entries))
	} else {
		fmt.Print(string(yamlBytes))
	}
}

// parseUpstreamsFromEnv reads all environment variables with the given prefix
// and parses them as host:port upstream definitions.
func parseUpstreamsFromEnv(prefix string) map[string]Upstream {
	result := make(map[string]Upstream)
	for _, env := range os.Environ() {
		if !strings.HasPrefix(env, prefix) {
			continue
		}
		parts := strings.SplitN(env, "=", 2)
		if len(parts) != 2 {
			continue
		}
		name := strings.ToLower(strings.TrimPrefix(parts[0], prefix))
		u, err := parseHostPort(parts[1])
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: ignoring %s: %v\n", parts[0], err)
			continue
		}
		result[name] = u
	}
	return result
}

// parseHostPort parses "host:port" into an Upstream.
func parseHostPort(s string) (Upstream, error) {
	parts := strings.SplitN(s, ":", 2)
	if len(parts) != 2 {
		return Upstream{}, fmt.Errorf("expected host:port, got %q", s)
	}
	port, err := strconv.ParseUint(parts[1], 10, 32)
	if err != nil {
		return Upstream{}, fmt.Errorf("invalid port in %q: %w", s, err)
	}
	return Upstream{Address: parts[0], Port: uint32(port)}, nil
}
