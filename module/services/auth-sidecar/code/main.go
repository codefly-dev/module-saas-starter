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
	"syscall"
	"time"

	codefly "github.com/codefly-dev/sdk-go"
	"github.com/codefly-dev/core/standards"

	authv3 "github.com/envoyproxy/go-control-plane/envoy/service/auth/v3"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/reflection"
	"google.golang.org/protobuf/types/known/emptypb"

	apigen "api/pkg/gen"
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
	if httpNet := codefly.For(ctx).WithDefaultNetwork().API(standards.REST).NetworkInstance(); httpNet != nil {
		httpPort = httpNet.Port
	}

	apiNet := codefly.For(ctx).Service("api").API("grpc").NetworkInstance()
	if apiNet == nil {
		panic("api gRPC endpoint not available")
	}
	apiAddr := fmt.Sprintf("%s:%d", apiNet.Hostname, apiNet.Port)

	// Upstream HTTP URLs used by the gateway listener.
	apiRestNet := codefly.For(ctx).Service("api").API("rest").NetworkInstance()
	apiHTTPURL := ""
	if apiRestNet != nil {
		apiHTTPURL = fmt.Sprintf("http://%s:%d", apiRestNet.Hostname, apiRestNet.Port)
	}
	apiConnectNet := codefly.For(ctx).Service("api").API("connect").NetworkInstance()
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

	publicKey := fetchPublicKey(ctx, apiConn)

	sidecar := NewSidecar(apiConn, publicKey)

	grpcServer := grpc.NewServer()
	authv3.RegisterAuthorizationServer(grpcServer, sidecar)
	reflection.Register(grpcServer)

	grpcLis, err := net.Listen("tcp", fmt.Sprintf(":%d", grpcPort))
	if err != nil {
		panic(fmt.Sprintf("failed to listen on grpc port: %v", err))
	}

	fmt.Printf("auth-sidecar gRPC (ext_authz) listening on :%d (api: %s, jwt: %v)\n",
		grpcPort, apiAddr, publicKey != nil)

	// Load route config.
	routeConfigPath := os.Getenv("ROUTE_CONFIG_PATH")
	routeConfig, err := LoadRouteConfig(routeConfigPath)
	if err != nil {
		panic(fmt.Sprintf("failed to load route config: %v", err))
	}
	matcher := NewRouteMatcher(routeConfig)

	// HTTP gateway listener — the single public ingress in dev.
	var httpServer *http.Server
	if httpPort > 0 {
		// Build upstream map from codefly network mappings.
		upstreams := make(map[string]*url.URL)
		if apiHTTPURL != "" {
			upstreams["api"] = MustURL(apiHTTPURL)
		}
		// If Connect URL is available, use it for api Connect paths;
		// otherwise fall back to the REST URL.
		if apiConnectURL != "" {
			// Connect routes also target "api" service — the matcher
			// resolves both REST and Connect to the same service name.
			// We prefer the Connect URL when available.
			// Note: both REST and Connect map to "api" in the route config.
			// The gateway uses the same upstream for both.
			if _, ok := upstreams["api"]; !ok {
				upstreams["api"] = MustURL(apiConnectURL)
			}
		}
		if frontendURL != "" {
			upstreams["frontend"] = MustURL(frontendURL)
		}

		rateLimiter := NewRateLimiter(1000) // 1000 req/min per org (or IP)
		defer rateLimiter.Stop()
		gateway := NewGateway(sidecar, matcher, upstreams, rateLimiter)
		httpServer = &http.Server{
			Addr:    fmt.Sprintf(":%d", httpPort),
			Handler: gateway,
		}
		go func() {
			fmt.Printf("auth-sidecar gateway (HTTP) listening on :%d (api: %s, frontend: %s, routes: %d)\n",
				httpPort, apiHTTPURL, frontendURL, len(routeConfig.Routes))
			if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				fmt.Fprintf(os.Stderr, "gateway http server error: %v\n", err)
			}
		}()
	}

	go func() {
		<-ctx.Done()
		grpcServer.GracefulStop()
		if httpServer != nil {
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = httpServer.Shutdown(shutdownCtx)
		}
	}()

	if err := grpcServer.Serve(grpcLis); err != nil {
		fmt.Fprintf(os.Stderr, "grpc server error: %v\n", err)
	}
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
