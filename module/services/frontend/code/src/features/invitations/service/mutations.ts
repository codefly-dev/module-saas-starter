import { useMutation, useQueryClient } from "@tanstack/react-query";
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
			role: string;
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
		mutationFn: (token: string) => svc.acceptInvitation({ token }),
	});
}
