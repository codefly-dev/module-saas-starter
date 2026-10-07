// Command role-catalog-import also ships as an accounts executable subcommand.
package main

import (
	"accounts/pkg/rolecatalogimport"
	"os"
)

func main() {
	os.Exit(rolecatalogimport.Run(os.Args[1:], os.Stdout, os.Stderr))
}
