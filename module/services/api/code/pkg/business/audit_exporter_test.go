package business

import (
	"testing"
)

// TestResolveEndpoint pins the scheme-based TLS toggle. The operator
// can flip TLS off by typing http:// in the endpoint field — used
// for local MinIO and any non-prod / on-prem deployment. Production
// deployments leave the scheme out (or use https://) and inherit
// the TLS-on default.
//
// Why it matters: before this helper landed, useSSL was hardcoded
// true and uploads to local MinIO at http://localhost:9000 would
// fail with TLS handshake errors that don't surface to the admin
// UI. The bug is subtle (errors land in last_error but the cycle
// retries until the operator notices); the test pins the contract.
func TestResolveEndpoint(t *testing.T) {
	tests := []struct {
		name       string
		endpoint   string
		region     string
		wantHost   string
		wantUseSSL bool
	}{
		{
			name:       "empty endpoint resolves to AWS S3 region host",
			endpoint:   "",
			region:     "us-west-2",
			wantHost:   "s3.us-west-2.amazonaws.com",
			wantUseSSL: true,
		},
		{
			name:       "bare host:port defaults to TLS on (production-shaped)",
			endpoint:   "minio.example.com:9000",
			region:     "us-east-1",
			wantHost:   "minio.example.com:9000",
			wantUseSSL: true,
		},
		{
			name:       "https:// strips scheme, keeps TLS on",
			endpoint:   "https://minio.example.com",
			region:     "us-east-1",
			wantHost:   "minio.example.com",
			wantUseSSL: true,
		},
		{
			name:       "http:// strips scheme, disables TLS",
			endpoint:   "http://localhost:9000",
			region:     "us-east-1",
			wantHost:   "localhost:9000",
			wantUseSSL: false,
		},
		{
			name:       "http:// IP form (local docker)",
			endpoint:   "http://127.0.0.1:9000",
			region:     "us-east-1",
			wantHost:   "127.0.0.1:9000",
			wantUseSSL: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotHost, gotSSL := resolveEndpoint(tt.endpoint, tt.region)
			if gotHost != tt.wantHost {
				t.Errorf("host: got %q, want %q", gotHost, tt.wantHost)
			}
			if gotSSL != tt.wantUseSSL {
				t.Errorf("useSSL: got %v, want %v", gotSSL, tt.wantUseSSL)
			}
		})
	}
}
