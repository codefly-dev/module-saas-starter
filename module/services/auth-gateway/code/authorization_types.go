package main

// Edge policy types are a deliberately narrow runtime projection of the
// Accounts descriptor policy. The Accounts catalog generator emits these
// values into authz_catalog_gen.go, so the gateway does not import the
// Accounts service implementation (or its Go module) merely to interpret
// generated routing metadata.
type edgeExposure uint8

const (
	edgeExposureUnspecified edgeExposure = iota
	edgeExposurePublic
	edgeExposureAuthenticated
	edgeExposureInternal
)

func (value edgeExposure) String() string {
	switch value {
	case edgeExposurePublic:
		return "public"
	case edgeExposureAuthenticated:
		return "authenticated"
	case edgeExposureInternal:
		return "internal"
	default:
		return "unspecified"
	}
}

type edgeRateLimitClass uint8

const (
	edgeRateLimitClassUnspecified edgeRateLimitClass = iota
	edgeRateLimitClassPublic
	edgeRateLimitClassAuthentication
	edgeRateLimitClassStandardRead
	edgeRateLimitClassStandardWrite
	edgeRateLimitClassSensitive
	edgeRateLimitClassWebhook
	edgeRateLimitClassInternal
	edgeRateLimitClassMFA
)
