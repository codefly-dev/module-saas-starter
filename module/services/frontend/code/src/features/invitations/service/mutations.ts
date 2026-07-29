import { useMutation, useQueryClient } from "@tanstack/react-query";
import type { InvitationRole } from "@/gen/saas/accounts/v1/invitations_pb";
import { useInvitationService } from "@/lib/hooks/use-api-client";

export function useCreateInvitation() {
	const svc = useInvitationService();
	const qc = useQueryClient();
	return useMutation({
		mutationFn: ({
			orgId,
			email,
			role,
		}: {
			orgId: string;
			email: string;
			role: InvitationRole;
		}) => svc.createInvitation({ orgId, email, role }),
		onSuccess: () => qc.invalidateQueries({ queryKey: ["invitations"] }),
	});
}

export function useRevokeInvitation() {
	const svc = useInvitationService();
	const qc = useQueryClient();
	return useMutation({
		mutationFn: (id: string) => svc.revokeInvitation({ id }),
		onSuccess: () => qc.invalidateQueries({ queryKey: ["invitations"] }),
	});
}

export function useAcceptInvitation() {
	const svc = useInvitationService();
	return useMutation({
		mutationFn: (token: string) =>
			svc.acceptInvitation({
				credential: { case: "token", value: token },
			}),
	});
}

export function useResendInvitation() {
	const svc = useInvitationService();
	const qc = useQueryClient();
	return useMutation({
		mutationFn: (id: string) =>
			svc.resendInvitation(
				{ id },
				{ headers: { "Idempotency-Key": crypto.randomUUID() } },
			),
		onSuccess: () => qc.invalidateQueries({ queryKey: ["invitations"] }),
	});
}
