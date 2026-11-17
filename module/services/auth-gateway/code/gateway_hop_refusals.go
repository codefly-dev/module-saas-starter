package main

import (
	"context"
	"log"
	"time"

	accountsv1 "auth-gateway/pkg/gen/saas/accounts/v1"

	"github.com/codefly-dev/authority/libraries/hop/go/forward"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

// The hop's own refusals reach the audit journal (owner decision 11).
//
// The authority tells its host each decision it makes, and accounts records
// its refusals. What the hop's front door refuses itself never reaches the
// authority: a call with no Work Context, a module this hop does not stand in
// front of, a method no published route serves, a check the authority did not
// answer in time, an admission that cannot be signed, no admission-key
// publication, a module that did not answer. They were a line of this
// gateway's log. In a cell this gateway is the hop and keeps no journal, so
// the front door's sink (forward.Config.Refused) hands each to accounts, which
// keeps the one journal there is (WorkContextService/RecordHopRefusal).
//
// A refusal the authority made and the hop passed on is not sent: the
// authority told its own host. Recording is best effort and bounded: the
// refusal stands and the caller is answered whether or not accounts took the
// row, and a row accounts did not take is a line of the log.

// recordHopRefusalMethod is accounts' internal procedure for it.
const recordHopRefusalMethod = "/saas.accounts.v1.WorkContextService/RecordHopRefusal"

// hopRefusalRecordTimeout bounds how long a refused call waits on its row.
const hopRefusalRecordTimeout = 2 * time.Second

// hopRefusalJournal is where the hop's own refusals are recorded.
type hopRefusalJournal interface {
	Record(ctx context.Context, req *accountsv1.RecordHopRefusalRequest) error
}

// accountsHopRefusals records through accounts' internal tier, by method name
// over the existing connection with the gateway's cluster-internal
// credential, as accountsSolutionEntitlements reads: there is no vendored
// client stub for the service, only the shared generated message types.
type accountsHopRefusals struct {
	conn          *grpc.ClientConn
	internalToken string
}

func (c *accountsHopRefusals) Record(ctx context.Context, req *accountsv1.RecordHopRefusalRequest) error {
	// The refused call's own context may already be over (a caller that
	// left, a deadline that passed: both are refusals worth their row).
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), hopRefusalRecordTimeout)
	defer cancel()
	ctx = metadata.AppendToOutgoingContext(ctx, "x-codefly-internal-token", c.internalToken)
	return c.conn.Invoke(ctx, recordHopRefusalMethod, req, &accountsv1.RecordHopRefusalResponse{})
}

// hopRefusalSink is the front door's sink over journal: every refusal the hop
// made itself is recorded, and the authority's own are left to the authority.
// No journal is no sink, which is the hop's log line alone.
func hopRefusalSink(journal hopRefusalJournal) forward.Sink {
	if journal == nil {
		return nil
	}
	return func(ctx context.Context, r forward.Refusal) {
		if r.ByAuthority {
			return
		}
		err := journal.Record(ctx, &accountsv1.RecordHopRefusalRequest{
			Method: r.Method, Code: r.Code.String(), Reason: r.Reason, Module: r.Module,
			OrganizationId: r.Organization, OwnerPrincipalId: r.Owner, ActorPrincipalId: r.Actor,
			BindingId: r.Binding, TaskId: r.Task, Claimed: r.Claimed,
		})
		if err != nil {
			// The reason is the journal's failure, never the refused call's
			// own, which the hop's log line carries.
			log.Printf("hop: a refusal of %s (%s) could not be recorded in the audit journal: %v", r.Method, r.Code, err)
		}
	}
}
