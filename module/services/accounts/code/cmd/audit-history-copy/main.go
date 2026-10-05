// Command audit-history-copy uses the same operator interface shipped in accounts.
package main

import (
	"accounts/pkg/auditops"
	"os"
)

func main() { os.Exit(auditops.RunHistory(os.Args[1:], os.Getenv, os.Stdout, os.Stderr)) }
