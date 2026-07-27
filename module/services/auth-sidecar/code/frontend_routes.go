package main

import (
	"net/http"
	"strings"
)

type frontendRouteMatch uint8

const (
	frontendRouteExact frontendRouteMatch = iota
	frontendRouteParameter
	frontendRouteCatchAll
)

type frontendRoutePattern struct {
	path  string
	match frontendRouteMatch
}

var frontendGETRouteHandlers = map[string]struct{}{
	"/api/fixtures":             {},
	"/api/notifications/stream": {},
	"/api/openapi":              {},
}

func matchFrontendGatewayRoute(method, path string) *RouteEntry {
	method = strings.ToUpper(method)
	if strings.HasPrefix(path, "/api/plugins/") {
		switch method {
		case http.MethodDelete, http.MethodGet, http.MethodHead, http.MethodOptions, http.MethodPatch, http.MethodPost, http.MethodPut:
			return frontendGatewayEntry(method, path)
		default:
			return nil
		}
	}
	if method != http.MethodGet && method != http.MethodHead {
		return nil
	}
	if strings.HasPrefix(path, "/_next/") {
		return frontendGatewayEntry(method, path)
	}
	if _, ok := frontendGETRouteHandlers[path]; ok {
		return frontendGatewayEntry(method, path)
	}
	for _, route := range generatedFrontendPageRoutes {
		if matchFrontendPath(route, path) {
			return frontendGatewayEntry(method, path)
		}
	}
	return nil
}

func frontendGatewayEntry(method, path string) *RouteEntry {
	return &RouteEntry{Service: "frontend", Method: method, Path: path, Protected: false}
}

func matchFrontendPath(route frontendRoutePattern, requestPath string) bool {
	pattern := strings.Split(route.path, "/")
	request := strings.Split(requestPath, "/")
	if route.match != frontendRouteCatchAll {
		return matchSegments(pattern, request)
	}
	if len(pattern) < 2 || len(request) < len(pattern) {
		return false
	}
	last := len(pattern) - 1
	if !strings.HasPrefix(pattern[last], "{*") || !strings.HasSuffix(pattern[last], "}") {
		return false
	}
	return matchSegments(pattern[:last], request[:last]) && strings.Join(request[last:], "/") != ""
}
