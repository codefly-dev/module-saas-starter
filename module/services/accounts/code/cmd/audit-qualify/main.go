// Command audit-qualify is also shipped as `accounts audit-qualify`.
package main

import (
	"accounts/pkg/auditops"
	"os"
)

func main() { os.Exit(auditops.RunQualification(os.Args[1:], os.Getenv, os.Stdout, os.Stderr)) }
