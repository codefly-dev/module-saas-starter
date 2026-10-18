package adapters

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"accounts/pkg/business"
	"accounts/pkg/infra"

	"github.com/codefly-dev/core/solutionhost"
	"github.com/stretchr/testify/require"
)

type appliedReadStore struct {
	deliveryNoopStore
	records []*business.SolutionHostBindingRecord
	reads   int
	err     error
}

func (s *appliedReadStore) ListSolutionHostBindings(context.Context) ([]*business.SolutionHostBindingRecord, error) {
	s.reads++
	return s.records, s.err
}

type appliedReadAuthorizer struct {
	fakeCarrierAuthorizer
	err        error
	credential string
}

func (a *appliedReadAuthorizer) AuthorizeAppliedObserver(_ context.Context, credential string) error {
	a.credential = credential
	return a.err
}

func TestAppliedObserverReportsOnlyCommittedGeneration(t *testing.T) {
	document := testModuleManifest(t, 3)
	store := &appliedReadStore{}
	observer := &appliedReadAuthorizer{}
	service, err := business.NewService(store)
	require.NoError(t, err)
	service.SetSolutionDelivery(nil, nil, observer)
	handler := NewSolutionDeliveryHTTPHandler(service)
	read := func(binding string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, SolutionDeliveryPrefix+"applied/"+binding, nil)
		r.Header.Set("Authorization", "Bearer publisher-token")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		require.Equal(t, "no-store", w.Header().Get("Cache-Control"))
		return w
	}
	require.Equal(t, http.StatusNotFound, read(document.Binding).Code)
	record := &business.SolutionHostBindingRecord{
		BindingID: document.Binding, HostCoordinate: "new-desired-host",
		Desired: &business.SolutionHostBindingGeneration{Generation: 999},
	}
	store.records = []*business.SolutionHostBindingRecord{record}
	require.Equal(t, http.StatusNotFound, read(document.Binding).Code, "desired acceptance cannot activate endpoints")
	for _, removed := range []bool{false, true} {
		document.Removed = removed
		if removed {
			document.Generation++
			document.Workloads, document.Endpoints = nil, nil
		}
		wire, marshalErr := solutionhost.Marshal(document)
		require.NoError(t, marshalErr)
		digest, digestErr := document.Digest()
		require.NoError(t, digestErr)
		at := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
		record.Applied = &business.SolutionHostBindingApplied{
			SolutionHostBindingGeneration: business.SolutionHostBindingGeneration{
				Generation: document.Generation, Digest: digest, Document: string(wire), At: at,
			}, Removed: removed, Release: document.Release.Identity(),
		}
		response := read(document.Binding)
		require.Equal(t, http.StatusOK, response.Code, response.Body.String())
		var got business.SolutionDeliveryApplied
		require.NoError(t, json.Unmarshal(response.Body.Bytes(), &got))
		require.Equal(t, business.SolutionDeliveryApplied{
			Host: document.Host, Binding: document.Binding, Generation: document.Generation,
			Digest: digest, Release: document.Release.Identity(), Removed: removed, AppliedAt: at,
		}, got)
	}
	// A newer generation received and withheld is reported with the host's
	// reason: delivered is not applied.
	record.Desired = &business.SolutionHostBindingGeneration{Generation: document.Generation + 1, Digest: "sha256:" + strings.Repeat("0", 64)}
	record.PendingReason = "module manifest generation was rewritten"
	var pending business.SolutionDeliveryApplied
	response := read(document.Binding)
	require.Equal(t, http.StatusOK, response.Code)
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &pending))
	require.Equal(t, &business.SolutionDeliveryPending{Generation: document.Generation + 1, Reason: record.PendingReason}, pending.Pending)
	record.Desired, record.PendingReason = nil, ""
	valid := *record.Applied
	for name, mutate := range map[string]func(*business.SolutionHostBindingApplied){
		"malformed":  func(a *business.SolutionHostBindingApplied) { a.Document = "invalid" },
		"generation": func(a *business.SolutionHostBindingApplied) { a.Generation++ },
		"digest":     func(a *business.SolutionHostBindingApplied) { a.Digest = "wrong" },
		"removal":    func(a *business.SolutionHostBindingApplied) { a.Removed = !a.Removed },
		"release":    func(a *business.SolutionHostBindingApplied) { a.Release = "wrong" },
		"time":       func(a *business.SolutionHostBindingApplied) { a.At = time.Time{} },
	} {
		copy := valid
		mutate(&copy)
		record.Applied = &copy
		require.Equal(t, http.StatusServiceUnavailable, read(document.Binding).Code, name)
	}
	record.Applied = &valid
	record.BindingID = "different-binding"
	require.Equal(t, http.StatusServiceUnavailable, read(record.BindingID).Code)
	record.BindingID = document.Binding
	store.err = errors.New("database unavailable")
	require.Equal(t, http.StatusServiceUnavailable, read(document.Binding).Code)
	store.err = nil
	for refusal, status := range map[error]int{
		infra.ErrCarrierUnauthenticated: http.StatusUnauthorized,
		infra.ErrCarrierNotAuthorized:   http.StatusForbidden,
		infra.ErrKubernetesUnavailable:  http.StatusServiceUnavailable,
	} {
		observer.err = refusal
		before := store.reads
		require.Equal(t, status, read(document.Binding).Code)
		require.Equal(t, status, read("unknown-binding").Code)
		require.Equal(t, before, store.reads, "refused callers cannot enumerate durable state")
	}
	require.Equal(t, "Bearer publisher-token", observer.credential)
}

func TestAppliedObserverHTTPBoundary(t *testing.T) {
	store := &appliedReadStore{}
	observer := &appliedReadAuthorizer{err: errors.New("unavailable")}
	service, err := business.NewService(store)
	require.NoError(t, err)
	service.SetSolutionDelivery(nil, nil, observer)
	handler := NewSolutionDeliveryHTTPHandler(service)
	for _, tc := range []struct {
		method, path string
		credentials  []string
		status       int
	}{
		{http.MethodGet, "applied/binding", nil, 401},
		{http.MethodGet, "applied/binding", []string{"Bearer a", "Bearer b"}, 401},
		{http.MethodPost, "applied/binding", []string{"Bearer a"}, 405},
		{http.MethodGet, "applied/binding/extra", []string{"Bearer a"}, 404},
		{http.MethodGet, "applied/", []string{"Bearer a"}, 404},
	} {
		r := httptest.NewRequest(tc.method, SolutionDeliveryPrefix+tc.path, nil)
		for _, credential := range tc.credentials {
			r.Header.Add("Authorization", credential)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		require.Equal(t, tc.status, w.Code)
		if tc.status == 405 {
			require.Equal(t, http.MethodGet, w.Header().Get("Allow"))
		}
	}
	require.Zero(t, store.reads)
}
