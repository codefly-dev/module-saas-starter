package main

import (
	"accounts/pkg/auditops"
	"accounts/pkg/rolecatalogimport"
	"os"
)

// Operator commands run before the generated service entry point. Keeping
// dispatch in authored code makes them part of the same immutable image and
// identity as the service without modifying agent-generated main.go/builders.
func init() {
	if len(os.Args) < 2 {
		return
	}
	switch os.Args[1] {
	case "role-catalog-import":
		os.Exit(rolecatalogimport.Run(os.Args[2:], os.Stdout, os.Stderr))
	case "audit-history-copy":
		os.Exit(auditops.RunHistory(os.Args[2:], os.Getenv, os.Stdout, os.Stderr))
	case "audit-qualify":
		os.Exit(auditops.RunQualification(os.Args[2:], os.Getenv, os.Stdout, os.Stderr))
	}
}
