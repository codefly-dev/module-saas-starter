package authzclient_test

import (
	"context"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/proto"

	"accounts/pkg/authzclient"
	gen "accounts/pkg/gen/saas/accounts/v1"
)

// capturingConn records the outgoing metadata of the last Invoke so the test
// can assert the service credential was attached.
type capturingConn struct {
	lastMD metadata.MD
	reply  *gen.CheckPermissionResponse
}

func (c *capturingConn) Invoke(ctx context.Context, _ string, _, reply any, _ ...grpc.CallOption) error {
	c.lastMD, _ = metadata.FromOutgoingContext(ctx)
	if out, ok := reply.(*gen.CheckPermissionResponse); ok && c.reply != nil {
		proto.Merge(out, c.reply)
	}
	return nil
}

func (c *capturingConn) NewStream(context.Context, *grpc.StreamDesc, string, ...grpc.CallOption) (grpc.ClientStream, error) {
	return nil, nil
}

// TestCheckPermissionAttachesServiceCredential proves the client makes the
// EXPOSURE_INTERNAL RPC callable by carrying the internal-token credential.
func TestCheckPermissionAttachesServiceCredential(t *testing.T) {
	conn := &capturingConn{reply: &gen.CheckPermissionResponse{Allowed: true, Reason: "granted"}}
	client := authzclient.New(conn, "s3cr3t-token")

	resp, err := client.CheckPermission(context.Background(), &gen.CheckPermissionRequest{
		SubjectId:   "11111111-1111-1111-1111-111111111111",
		SubjectKind: gen.SubjectKind_SUBJECT_KIND_PRINCIPAL,
		Resource:    "deployments",
		Action:      "write",
		OrgId:       "22222222-2222-2222-2222-222222222222",
		Scope:       "module-a",
	})
	if err != nil {
		t.Fatalf("CheckPermission: %v", err)
	}
	if !resp.Allowed {
		t.Fatalf("expected allowed decision, got %+v", resp)
	}

	got := conn.lastMD.Get(authzclient.InternalTokenHeader)
	if len(got) != 1 || got[0] != "s3cr3t-token" {
		t.Fatalf("service credential not attached: %v", got)
	}
}

// TestCheckPermissionRequiresCredential proves a caller without the credential
// is rejected client-side rather than issuing an unauthenticated call.
func TestCheckPermissionRequiresCredential(t *testing.T) {
	conn := &capturingConn{}
	client := authzclient.New(conn, "")

	_, err := client.CheckPermission(context.Background(), &gen.CheckPermissionRequest{
		SubjectId: "11111111-1111-1111-1111-111111111111",
		Resource:  "deployments",
		Action:    "write",
	})
	if err == nil {
		t.Fatal("expected error when service credential is empty")
	}
	if conn.lastMD != nil {
		t.Fatal("no call should have been issued without a credential")
	}
}
