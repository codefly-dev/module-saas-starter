"use client";

import { useMemo } from "react";
import { createClient } from "@connectrpc/connect";
import { apiTransport } from "@/lib/connect/transport";
import {
  UserService,
  OrganizationService,
  TeamService,
  PermissionService,
  AuthService,
  AuditService,
  APIKeyService,
  PlatformAdminService,
  InvitationService,
} from "@/gen/user_pb";

export function useUserService() {
  return useMemo(() => createClient(UserService, apiTransport), []);
}

export function useOrganizationService() {
  return useMemo(() => createClient(OrganizationService, apiTransport), []);
}

export function useTeamService() {
  return useMemo(() => createClient(TeamService, apiTransport), []);
}

export function usePermissionService() {
  return useMemo(() => createClient(PermissionService, apiTransport), []);
}

export function useAuthService() {
  return useMemo(() => createClient(AuthService, apiTransport), []);
}

export function useAuditService() {
  return useMemo(() => createClient(AuditService, apiTransport), []);
}

export function useAPIKeyService() {
  return useMemo(() => createClient(APIKeyService, apiTransport), []);
}

export function usePlatformAdminService() {
  return useMemo(() => createClient(PlatformAdminService, apiTransport), []);
}

export function useInvitationService() {
  return useMemo(() => createClient(InvitationService, apiTransport), []);
}
