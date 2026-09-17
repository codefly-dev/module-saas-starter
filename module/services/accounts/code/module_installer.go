package main

import (
	"accounts/pkg/adapters"
	"accounts/pkg/business"
	"net/http"
	"os"
)

func configuredModuleInstaller(service *business.Service) (http.Handler, error) {
	path := os.Getenv("MODULE_INSTALLER_POLICY_FILE")
	if path == "" {
		return nil, nil
	}
	return adapters.NewModuleInstallationHTTPHandler(service, path)
}
