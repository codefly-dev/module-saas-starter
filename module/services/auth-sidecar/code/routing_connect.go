package main

// Connect RPC route discovery.
//
// Unlike REST routes (which come from folder-based *.rest.codefly.yaml files),
// Connect paths are mechanically derived from the proto service definitions:
// each `rpc Foo` in `service BarService` in `package customers` becomes the
// path `/customers.BarService/Foo`. We discover these by importing the api
// proto package (already a dependency) and walking protoregistry.GlobalFiles.

import (
	"context"
	"fmt"
	"log"
	"strings"

	_ "accounts/pkg/gen" // triggers proto descriptor registration for customers.*

	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
)

// publicConnectRPCs lists the fully-qualified Connect paths that must remain
// reachable without authentication. Everything else in the customers package
// is protected. Mirrors the extension.protected:false entries in
// routing/rest/saas-starter/api/auth.rest.codefly.yaml.
var publicConnectRPCs = map[string]bool{
	"/customers.AuthService/Authenticate": true,
	"/customers.AuthService/RefreshToken": true,
	"/customers.AuthService/Logout":       true,
	"/customers.AuthService/GetJWKS":      true,
}

// LoadConnectRoutesFromProto walks the proto registry and builds a RouteEntry
// for every RPC in every service whose package starts with packagePrefix.
func LoadConnectRoutesFromProto(ctx context.Context, packagePrefix string) ([]*RouteEntry, error) {
	var entries []*RouteEntry

	protoregistry.GlobalFiles.RangeFiles(func(fd protoreflect.FileDescriptor) bool {
		if !strings.HasPrefix(string(fd.Package()), packagePrefix) {
			return true
		}
		services := fd.Services()
		for i := 0; i < services.Len(); i++ {
			svc := services.Get(i)
			fqService := string(svc.FullName()) // e.g. "customers.UserService"
			methods := svc.Methods()
			for j := 0; j < methods.Len(); j++ {
				method := methods.Get(j)
				path := fmt.Sprintf("/%s/%s", fqService, method.Name())
				entries = append(entries, &RouteEntry{
					Service:   "api_connect",
					Method:    "POST",
					Path:      path,
					Protected: !publicConnectRPCs[path],
				})
			}
		}
		return true
	})

	log.Printf("routing: discovered %d Connect RPC paths from proto package prefix %q", len(entries), packagePrefix)
	return entries, nil
}
