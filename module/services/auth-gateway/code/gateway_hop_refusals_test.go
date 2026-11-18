package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	accountsv1 "auth-gateway/pkg/gen/saas/accounts/v1"

	hop "github.com/codefly-dev/authority/libraries/hop/go"
	"github.com/codefly-dev/authority/libraries/hop/go/hoptest"
	"github.com/codefly-dev/authority/libraries/hop/go/routing"
	basev0 "github.com/codefly-dev/core/generated/go/codefly/base/v0"
	core "github.com/codefly-dev/core/workcontext"
	"github.com/stretchr/testify/require"

	"encoding/json"
)

// recordedHopRefusals is accounts' journal as the gateway reaches it.
type recordedHopRefusals struct {
	mu       sync.Mutex
	recorded []*accountsv1.RecordHopRefusalRequest
	fail     error
}

func (j *recordedHopRefusals) Record(_ context.Context, req *accountsv1.RecordHopRefusalRequest) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.fail != nil {
		return j.fail
	}
	j.recorded = append(j.recorded, req)
	return nil
}

func (j *recordedHopRefusals) all() []*accountsv1.RecordHopRefusalRequest {
	j.mu.Lock()
	defer j.mu.Unlock()
	return append([]*accountsv1.RecordHopRefusalRequest(nil), j.recorded...)
}

// hopWithJournal stands the hop in front of documents, whose listener is
// module, recording what the front door refuses in journal.
func hopWithJournal(t *testing.T, module http.Handler, journal hopRefusalJournal) (*hopRole, *hostAuthority) {
	t.Helper()
	t.Setenv("CODEFLY__ENVIRONMENT", "local")
	accounts := &hostAuthority{signer: hoptest.New(t), t: t}
	moduleServer := httptest.NewServer(module)
	t.Cleanup(moduleServer.Close)
	routes := &routing.Routing{Schema: routing.RoutingSchema, Endpoints: []routing.RouteEndpoint{{
		Audience: "documents", Service: "documents", Endpoint: "connect", Transport: "connect",
		Address: moduleServer.URL, Package: "documents", Version: "0.1.0", Digest: "sha256:d",
		Methods: []string{listCollection},
	}}}
	raw, err := json.Marshal(routes)
	require.NoError(t, err)
	role, front, err := newHopRole(t.Context(), accounts, workloadCredential, []string{"documents"}, string(raw), routes, "", hopRefusalSink(journal))
	require.NoError(t, err)
	t.Cleanup(front.Close)
	return role, accounts
}

// Decision 11, the hop's half: a call the front door refuses itself never
// reaches the authority, so the gateway, which is the hop in a cell and keeps
// no journal, hands it to accounts, which keeps the one there is. A refusal
// the authority made is the authority's to record, and is not sent twice.
func TestTheHopsOwnRefusalsAreRecordedAndTheAuthoritysAreNot(t *testing.T) {
	journal := &recordedHopRefusals{}
	reached := 0
	role, accounts := hopWithJournal(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { reached++ }), journal)
	call := func(token string) int {
		req := listCollectionRequest(listCollection)
		if token != "" {
			req.Header.Set(hop.WorkContextKey, token)
		}
		w := httptest.NewRecorder()
		role.face.ServeHTTP(w, req)
		return w.Code
	}

	// No Work Context: the hop's own refusal, about nobody.
	require.Equal(t, http.StatusUnauthorized, call(""))
	recorded := journal.all()
	require.Len(t, recorded, 1)
	require.Contains(t, recorded[0].GetMethod(), listCollection)
	require.Equal(t, "Unauthenticated", recorded[0].GetCode())
	require.NotEmpty(t, recorded[0].GetReason())
	require.Empty(t, recorded[0].GetOrganizationId())
	require.Empty(t, recorded[0].GetOwnerPrincipalId())
	require.False(t, recorded[0].GetClaimed())

	// A Work Context addressed to a module this hop does not stand in front
	// of: refused by the hop before the authority is asked, so what the
	// Work Context says of itself is a claim.
	elsewhere := accounts.signer.Issue(t, "records", &basev0.WorkScopeV1{ResourceKind: "records", Actions: []string{"read"}})
	require.NotEqual(t, http.StatusOK, call(elsewhere))
	recorded = journal.all()
	require.Len(t, recorded, 2)
	decoded, err := core.Decode(elsewhere)
	require.NoError(t, err)
	require.Equal(t, "records", recorded[1].GetModule())
	require.Equal(t, decoded.Context().GetOwnerPrincipalId(), recorded[1].GetOwnerPrincipalId())
	require.Equal(t, decoded.Context().GetTaskId(), recorded[1].GetTaskId())
	require.True(t, recorded[1].GetClaimed(), "read from the Work Context, checked by nobody")
	require.Zero(t, reached)

	// The authority's own refusal is passed on and not recorded here: the
	// authority told its host.
	accounts.refuse = true
	admitted := accounts.signer.Issue(t, "documents", &basev0.WorkScopeV1{ResourceKind: "documents", Actions: []string{"read"}})
	require.Equal(t, http.StatusForbidden, call(admitted))
	require.Len(t, journal.all(), 2, "the authority's refusal is not the hop's to record")
	require.Zero(t, reached)

	// A journal that cannot be reached changes no answer: the refusal stands.
	journal.fail = errors.New("accounts is unreachable")
	require.Equal(t, http.StatusUnauthorized, call(""))
	require.Len(t, journal.all(), 2)
}

// With no journal the hop refuses as it always did, to its log alone.
func TestAHopWithNoJournalStillRefuses(t *testing.T) {
	require.Nil(t, hopRefusalSink(nil))
	role, _ := hopWithJournal(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}), nil)
	w := httptest.NewRecorder()
	role.face.ServeHTTP(w, listCollectionRequest(listCollection))
	require.Equal(t, http.StatusUnauthorized, w.Code)
}
