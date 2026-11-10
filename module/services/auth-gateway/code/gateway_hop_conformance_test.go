package main

import (
	"context"
	"net/http"
	"strings"
	"testing"

	authorityv1 "github.com/codefly-dev/authority/libraries/authority-client/go/gen/host/authority/v1"
	hopconformance "github.com/codefly-dev/authority/libraries/conformance/hop/go"
	"github.com/codefly-dev/authority/libraries/hop/go/forward"
	"github.com/codefly-dev/authority/libraries/hop/go/routing"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// suiteAuthority is the suite's authority as hopAuthorityClient: the front
// door's Check and RegisterAdmissionKey. Issue is the browser path's, which
// the front door cases never take.
type suiteAuthority struct{ forward.Authority }

func (suiteAuthority) Issue(context.Context, *authorityv1.IssueRequest, ...grpc.CallOption) (*authorityv1.IssueResponse, error) {
	return nil, status.Error(codes.Unimplemented, "the hop front door cases issue no Work Context through the gateway")
}

// The gateway standing as the hop on a cell passes the shared front door
// target: the hop endpoint's face (hopFace), built by newHopRole from what
// the composition hands it, refuses a Work Context addressed to another
// fronted module as PermissionDenied naming both, and keeps NotFound for a
// procedure no fronted module publishes.
func TestHopTheGatewayPassesTheHopFrontTarget(t *testing.T) {
	hopconformance.RunFront(t, hopconformance.FrontTarget{Stand: func(ctx context.Context, authority forward.Authority, modules []string, raw string) (http.Handler, func(), error) {
		routes, err := routing.DecodeRouting(raw)
		if err != nil {
			return nil, nil, err
		}
		role, front, err := newHopRole(ctx, suiteAuthority{authority}, workloadCredential, hopModules(strings.Join(modules, ",")), raw, routes, "")
		if err != nil {
			return nil, nil, err
		}
		grpcFace := grpc.NewServer(front.ServerOptions...)
		return hopFace(grpcFace, role.face), func() { grpcFace.Stop(); front.Close() }, nil
	}})
}
