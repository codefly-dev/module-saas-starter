package main

import (
	"context"
	"errors"
	"net/http"
)

// The gateway's `workload` endpoint is the one its composed workloads reach it
// on: plain HTTP on its own port, serving the gateway's full handler, admitted
// by the cell's mesh from composed workloads alone (and bound beside the rest
// of a native run locally). The module credential exchanges are served there
// and nowhere else; the public `rest` endpoint answers 404 to them.
//
// Only the server bound to that endpoint marks its requests. A header never
// does: the mark is a property of the socket a request arrived on, and a
// caller on the public endpoint cannot write one.

type workloadListenerKey struct{}

// onWorkloadListener reports whether the request arrived on the workload
// endpoint.
func onWorkloadListener(ctx context.Context) bool {
	marked, _ := ctx.Value(workloadListenerKey{}).(bool)
	return marked
}

// workloadListener marks every request the workload endpoint's server serves.
func workloadListener(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), workloadListenerKey{}, true)))
	})
}

func authenticatedWorkloadListener(next http.Handler, waypoint string) (http.Handler, error) {
	if _, err := presenterFromXFCC([]string{"By=" + waypoint + ";URI=" + waypoint}, waypoint); err != nil {
		return nil, errors.New("protected workload presenter waypoint must be a canonical SPIFFE identity")
	}
	return workloadListener(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		principal, err := presenterFromXFCC(r.Header.Values("X-Forwarded-Client-Cert"), waypoint)
		if err != nil {
			httpError(w, http.StatusUnauthorized, "workload presenter required")
			return
		}
		r.Header.Del("X-Forwarded-Client-Cert")
		r.Header.Del(workloadPresenterHeader)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), workloadPresenterKey{}, principal)))
	})), nil
}
