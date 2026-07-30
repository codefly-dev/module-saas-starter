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
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	_, err := codefly.Init(ctx)
	if err != nil {
		panic(fmt.Sprintf("codefly init failed: %v", err))
	}

	grpcPort := codefly.For(ctx).WithDefaultNetwork().API(standards.GRPC).NetworkInstance().Port
	var httpPort uint16
	httpNet, httpNetErr := codefly.For(ctx).API(standards.REST).ResolveNetworkInstance()
	if httpNetErr != nil || httpNet == nil {
		panic(fmt.Sprintf("Codefly REST endpoint is unavailable: %v", httpNetErr))
	}
	httpPort = httpNet.Port

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

	// Private HTTP API gateway listener behind the frontend product entry.
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
		gateway := NewGateway(sidecar, matcher, upstreams, rateLimiter)
		httpServer = &http.Server{
			Addr:    fmt.Sprintf(":%d", httpPort),
			Handler: gateway,
		}
		go func() {
			fmt.Printf("auth-sidecar API gateway (HTTP) listening on :%d (api: %s, routes: %d)\n",
				httpPort, apiHTTPURL, len(routeEntries))
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
