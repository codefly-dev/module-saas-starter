//go:build integration

package business

import "accounts/pkg/datasource/apisource"

// SetSourceOperationIntegrationClient exposes the existing provider transport
// seam to cross-package integration tests. Identity, authority, admission,
// cipher and persistence remain real. Normal service builds exclude this file.
func (s *Service) SetSourceOperationIntegrationClient(fn func(apisource.Config, string) APIOperationClient) {
	s.newAPIOperationClient = fn
}
