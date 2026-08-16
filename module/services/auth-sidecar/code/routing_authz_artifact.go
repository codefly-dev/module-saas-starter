package main

// Runtime-loadable edge authorization metadata.
//
// applyGeneratedAuthorizationMetadata joins every route to a method policy
// projection. That projection is compiled into authz_catalog_gen.go, so a new
// procedure — a route whose backend lives in another module's plugin — cannot
// be authorized without an auth-sidecar rebuild. When AUTHZ_METADATA_CATALOG_PATH
// names a deployed artifact, the projection is read and validated from that JSON
// at startup instead, so policy can ship with the plugin. The artifact is the
// same saas.authz.methods.v1 document the catalog generator already emits; a
// runtime load produces the same generatedAuthorizationMetadata values the
// compiled map holds. Only the edge-relevant policy fields are modeled — the
// generator owns the deep policy validation and the policy fingerprint.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"
)

const (
	authzMetadataCatalogEnv   = "AUTHZ_METADATA_CATALOG_PATH"
	authzMethodsSchemaVersion = "saas.authz.methods.v1"
	rateLimitBackendFailClose = "RATE_LIMIT_BACKEND_FAILURE_MODE_FAIL_CLOSED"
)

var edgeExposureByName = map[string]edgeExposure{
	"EXPOSURE_PUBLIC":        edgeExposurePublic,
	"EXPOSURE_AUTHENTICATED": edgeExposureAuthenticated,
	"EXPOSURE_INTERNAL":      edgeExposureInternal,
}

var edgeRateLimitClassByName = map[string]edgeRateLimitClass{
	"RATE_LIMIT_CLASS_PUBLIC":         edgeRateLimitClassPublic,
	"RATE_LIMIT_CLASS_AUTHENTICATION": edgeRateLimitClassAuthentication,
	"RATE_LIMIT_CLASS_STANDARD_READ":  edgeRateLimitClassStandardRead,
	"RATE_LIMIT_CLASS_STANDARD_WRITE": edgeRateLimitClassStandardWrite,
	"RATE_LIMIT_CLASS_SENSITIVE":      edgeRateLimitClassSensitive,
	"RATE_LIMIT_CLASS_WEBHOOK":        edgeRateLimitClassWebhook,
	"RATE_LIMIT_CLASS_INTERNAL":       edgeRateLimitClassInternal,
	"RATE_LIMIT_CLASS_MFA":            edgeRateLimitClassMFA,
}

type authzMethodsDocument struct {
	SchemaVersion string             `json:"schema_version"`
	Owner         *routeCatalogOwner `json:"owner"`
	Methods       []authzMethodItem  `json:"methods"`
}

type authzMethodItem struct {
	Procedure                   string           `json:"procedure"`
	Policy                      *authzEdgePolicy `json:"policy"`
	PolicySHA256                string           `json:"policy_sha256"`
	RateLimitBackendFailureMode string           `json:"rate_limit_backend_failure_mode"`
}

type authzEdgePolicy struct {
	Exposure                    string `json:"exposure"`
	RateLimit                   string `json:"rate_limit"`
	AuthenticationFactorAttempt bool   `json:"authentication_factor_attempt"`
}

// authorizationByProcedure returns the method policy projection, preferring the
// deployed artifact when AUTHZ_METADATA_CATALOG_PATH names one.
func authorizationByProcedure() (map[string]generatedAuthorizationMetadata, error) {
	path := strings.TrimSpace(os.Getenv(authzMetadataCatalogEnv))
	if path == "" {
		return generatedAuthorizationByProcedure, nil
	}
	metadata, err := loadAuthorizationMetadataFromArtifact(path)
	if err != nil {
		return nil, err
	}
	log.Printf("routing: loaded %d authorization methods from artifact %s", len(metadata), path)
	return metadata, nil
}

// loadAuthorizationMetadataFromArtifact reads and validates a saas.authz.methods.v1
// artifact, rejecting schema drift, incomplete identities, unknown policy enums,
// and malformed fingerprints so an invalid artifact fails the gateway closed.
func loadAuthorizationMetadataFromArtifact(path string) (map[string]generatedAuthorizationMetadata, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("routing: cannot read authorization artifact %s: %w", path, err)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	var doc authzMethodsDocument
	if err := decoder.Decode(&doc); err != nil {
		return nil, fmt.Errorf("routing: cannot decode authorization artifact %s: %w", path, err)
	}
	if doc.SchemaVersion != authzMethodsSchemaVersion {
		return nil, fmt.Errorf("routing: authorization artifact %s has schema version %q, want %q", path, doc.SchemaVersion, authzMethodsSchemaVersion)
	}
	if doc.Owner == nil || doc.Owner.Module == "" || doc.Owner.Service == "" {
		return nil, fmt.Errorf("routing: authorization artifact %s owner is incomplete", path)
	}
	if len(doc.Methods) == 0 {
		return nil, fmt.Errorf("routing: authorization artifact %s contains no methods", path)
	}
	metadata := make(map[string]generatedAuthorizationMetadata, len(doc.Methods))
	for index := range doc.Methods {
		method := &doc.Methods[index]
		if method.Procedure == "" || method.Policy == nil {
			return nil, fmt.Errorf("routing: authorization artifact %s has a method with an incomplete identity", path)
		}
		if _, exists := metadata[method.Procedure]; exists {
			return nil, fmt.Errorf("routing: authorization artifact %s has duplicate procedure %q", path, method.Procedure)
		}
		if !isPolicySHA256(method.PolicySHA256) {
			return nil, fmt.Errorf("routing: authorization artifact %s procedure %q has an invalid policy fingerprint", path, method.Procedure)
		}
		exposure, ok := edgeExposureByName[method.Policy.Exposure]
		if !ok {
			return nil, fmt.Errorf("routing: authorization artifact %s procedure %q has unsupported exposure %q", path, method.Procedure, method.Policy.Exposure)
		}
		rateLimit, ok := edgeRateLimitClassByName[method.Policy.RateLimit]
		if !ok {
			return nil, fmt.Errorf("routing: authorization artifact %s procedure %q has unsupported rate-limit class %q", path, method.Procedure, method.Policy.RateLimit)
		}
		metadata[method.Procedure] = generatedAuthorizationMetadata{
			exposure:                    exposure,
			rateLimitClass:              rateLimit,
			rateLimitBackendFailClosed:  method.RateLimitBackendFailureMode == rateLimitBackendFailClose,
			authenticationFactorAttempt: method.Policy.AuthenticationFactorAttempt,
			policySHA256:                method.PolicySHA256,
		}
	}
	return metadata, nil
}

func isPolicySHA256(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, char := range value {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return false
		}
	}
	return true
}
