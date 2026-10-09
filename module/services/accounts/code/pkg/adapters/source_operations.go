package adapters

import (
	"context"
	"errors"

	"accounts/pkg/datasource/operations"
	gen "accounts/pkg/gen/saas/accounts/v1"
	"connectrpc.com/connect"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/structpb"
)

func declarationFromProto(in *gen.SourceOperation) (operations.Declaration, error) {
	if in == nil || in.InputSchema == nil || in.OutputSchema == nil {
		return operations.Declaration{}, errors.New("operation schemas required")
	}
	input, err := protojson.Marshal(in.InputSchema)
	if err != nil {
		return operations.Declaration{}, errors.New("invalid input schema")
	}
	output, err := protojson.Marshal(in.OutputSchema)
	if err != nil {
		return operations.Declaration{}, errors.New("invalid output schema")
	}
	effect := ""
	switch in.Effect {
	case gen.SourceOperation_EFFECT_READ_ONLY:
		effect = operations.ReadOnly
	case gen.SourceOperation_EFFECT_MUTATION:
		effect = operations.Mutation
	}
	return operations.Declaration{Name: in.Name, Description: in.Description, Method: in.Method, Path: in.Path, Query: in.Query, InputSchema: input, OutputSchema: output, Effect: effect, MaxOutputBytes: int(in.MaxOutputBytes), Digest: in.Digest}, nil
}
func declarationToProto(in operations.Declaration) (*gen.SourceOperation, error) {
	input := &structpb.Struct{}
	output := &structpb.Struct{}
	if err := protojson.Unmarshal(in.InputSchema, input); err != nil {
		return nil, errors.New("stored input schema invalid")
	}
	if err := protojson.Unmarshal(in.OutputSchema, output); err != nil {
		return nil, errors.New("stored output schema invalid")
	}
	effect := gen.SourceOperation_EFFECT_UNSPECIFIED
	switch in.Effect {
	case operations.ReadOnly:
		effect = gen.SourceOperation_EFFECT_READ_ONLY
	case operations.Mutation:
		effect = gen.SourceOperation_EFFECT_MUTATION
	}
	return &gen.SourceOperation{Name: in.Name, Description: in.Description, Method: in.Method, Path: in.Path, Query: in.Query, InputSchema: input, OutputSchema: output, Effect: effect, MaxOutputBytes: uint32(in.MaxOutputBytes), Digest: in.Digest}, nil
}
func declarationsToProto(in []operations.Declaration) ([]*gen.SourceOperation, error) {
	out := make([]*gen.SourceOperation, 0, len(in))
	for _, d := range in {
		p, err := declarationToProto(d)
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal, err)
		}
		out = append(out, p)
	}
	return out, nil
}
func (h *datasourceConnectHandler) DeclareSourceOperations(ctx context.Context, req *connect.Request[gen.DeclareSourceOperationsRequest]) (*connect.Response[gen.DeclareSourceOperationsResponse], error) {
	ctx = connectCtx(ctx, req.Header())
	actor, err := callerID(ctx)
	if err != nil {
		return nil, err
	}
	if err := requireOrgAdmin(ctx, actor, req.Msg.OrgId); err != nil {
		return nil, translateGRPCError(err)
	}
	declarations := make([]operations.Declaration, 0, len(req.Msg.Operations))
	for _, in := range req.Msg.Operations {
		d, err := declarationFromProto(in)
		if err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, err)
		}
		declarations = append(declarations, d)
	}
	out, err := h.svc.DeclareSourceOperations(ctx, actor, req.Msg.OrgId, req.Msg.SourceId, declarations)
	if err != nil {
		return nil, translateGRPCError(err)
	}
	projected, err := declarationsToProto(out)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&gen.DeclareSourceOperationsResponse{Operations: projected}), nil
}
func (h *datasourceConnectHandler) ListSourceOperations(ctx context.Context, req *connect.Request[gen.ListSourceOperationsRequest]) (*connect.Response[gen.ListSourceOperationsResponse], error) {
	ctx = connectCtx(ctx, req.Header())
	actor, err := callerID(ctx)
	if err != nil {
		return nil, err
	}
	if err := requireOrgMember(ctx, actor, req.Msg.OrgId); err != nil {
		return nil, translateGRPCError(err)
	}
	out, err := h.svc.ListSourceOperations(ctx, actor, req.Msg.OrgId, req.Msg.SourceId)
	if err != nil {
		return nil, translateGRPCError(err)
	}
	projected, err := declarationsToProto(out)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&gen.ListSourceOperationsResponse{Operations: projected}), nil
}
