package adapters

import (
	"context"
	"errors"

	"accounts/pkg/business"
	gen "accounts/pkg/gen/saas/accounts/v1"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
)

// MFAServer gives the MFA surface the same native gRPC and grpc-gateway
// coverage as AuthService. Connect uses the same business methods directly.
type MFAServer struct {
	gen.UnsafeMFAServiceServer
}

func (s *MFAServer) BeginWebAuthnRegistration(ctx context.Context, req *gen.BeginWebAuthnRegistrationRequest) (*gen.BeginWebAuthnRegistrationResponse, error) {
	if err := Validate(req); err != nil {
		return nil, err
	}
	userID, err := requireAuth(ctx)
	if err != nil {
		return nil, err
	}
	token, options, err := service.BeginWebAuthnRegistration(ctx, userID)
	if err != nil {
		return nil, err
	}
	return &gen.BeginWebAuthnRegistrationResponse{CeremonyToken: token, PublicKeyOptionsJson: options}, nil
}

func (s *MFAServer) FinishWebAuthnRegistration(ctx context.Context, req *gen.FinishWebAuthnRegistrationRequest) (*gen.FinishWebAuthnRegistrationResponse, error) {
	if err := Validate(req); err != nil {
		return nil, err
	}
	userID, err := requireAuth(ctx)
	if err != nil {
		return nil, err
	}
	device, err := service.FinishWebAuthnRegistration(ctx, userID, req.CeremonyToken, req.CredentialResponseJson, req.Name)
	if errors.Is(err, business.ErrWebAuthnCeremonyRejected) {
		return nil, status.Error(codes.InvalidArgument, "WebAuthn ceremony rejected")
	}
	if err != nil {
		return nil, err
	}
	return &gen.FinishWebAuthnRegistrationResponse{Device: mfaDeviceToProto(device)}, nil
}

func (s *MFAServer) SetupTOTP(ctx context.Context, req *gen.SetupTOTPRequest) (*gen.SetupTOTPResponse, error) {
	if err := Validate(req); err != nil {
		return nil, err
	}
	userID, err := requireAuth(ctx)
	if err != nil {
		return nil, err
	}
	secret, uri, err := service.SetupTOTP(ctx, userID)
	if err != nil {
		return nil, err
	}
	return &gen.SetupTOTPResponse{Secret: secret, ProvisioningUri: uri}, nil
}

func (s *MFAServer) VerifyTOTP(ctx context.Context, req *gen.VerifyTOTPRequest) (*gen.VerifyTOTPResponse, error) {
	if err := Validate(req); err != nil {
		return nil, err
	}
	userID, err := requireAuth(ctx)
	if err != nil {
		return nil, err
	}
	if err := service.VerifyTOTP(ctx, userID, req.Code); err != nil {
		return &gen.VerifyTOTPResponse{Valid: false}, nil
	}
	return &gen.VerifyTOTPResponse{Valid: true}, nil
}

func (s *MFAServer) ListDevices(ctx context.Context, req *gen.ListMFADevicesRequest) (*gen.ListMFADevicesResponse, error) {
	if err := Validate(req); err != nil {
		return nil, err
	}
	userID, err := requireAuth(ctx)
	if err != nil {
		return nil, err
	}
	devices, err := service.ListMFADevices(ctx, userID)
	if err != nil {
		return nil, err
	}
	out := make([]*gen.MFADevice, 0, len(devices))
	for _, device := range devices {
		out = append(out, mfaDeviceToProto(device))
	}
	return &gen.ListMFADevicesResponse{Devices: out}, nil
}

func (s *MFAServer) RevokeDevice(ctx context.Context, req *gen.RevokeMFADeviceRequest) (*emptypb.Empty, error) {
	if err := Validate(req); err != nil {
		return nil, err
	}
	userID, err := requireAuth(ctx)
	if err != nil {
		return nil, err
	}
	if err := service.RevokeMFADevice(ctx, userID, req.Id); err != nil {
		return nil, err
	}
	return &emptypb.Empty{}, nil
}

func (s *MFAServer) GenerateBackupCodes(ctx context.Context, req *gen.GenerateBackupCodesRequest) (*gen.GenerateBackupCodesResponse, error) {
	if err := Validate(req); err != nil {
		return nil, err
	}
	userID, err := requireAuth(ctx)
	if err != nil {
		return nil, err
	}
	codes, err := service.GenerateBackupCodes(ctx, userID)
	if err != nil {
		return nil, err
	}
	return &gen.GenerateBackupCodesResponse{BackupCodes: codes}, nil
}
