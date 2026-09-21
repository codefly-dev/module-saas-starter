/** Fixed preview identity; never used by the product build. */
const identity = {
	user: {
		id: "00000000-0000-4000-8000-000000000001",
		email: "jane@example.com",
		name: "Jane Doe",
	},
	organizationId: "example-org",
	platformRole: "super_admin",
	orgRole: "owner",
	isAuthenticated: true,
	isLoading: false,
	impersonation: { isImpersonating: false },
	switchOrganization: async (organizationId: string) => {
		void organizationId;
	},
	getToken: () => null,
} as const;
export function useAuth() {
	return identity;
}
