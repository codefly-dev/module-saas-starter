async function postBillingAction(
	path: string,
	token: string | null,
	body?: Record<string, string>,
): Promise<Record<string, string>> {
	if (!token) throw new Error("Authentication required");
	const response = await fetch(path, {
		method: "POST",
		headers: {
			Authorization: `Bearer ${token}`,
			"Content-Type": "application/json",
			"Idempotency-Key": crypto.randomUUID(),
		},
		body: body ? JSON.stringify(body) : undefined,
	});
	const payload = (await response.json()) as Record<string, string>;
	if (!response.ok) {
		throw new Error(payload.error || "Billing action failed");
	}
	return payload;
}

export const billingMutations = {
	async startCheckout(token: string | null, planName: string): Promise<string> {
		const response = await postBillingAction("/v1/billing/checkout", token, {
			plan_name: planName,
		});
		if (!response.url) throw new Error("Checkout URL was not returned");
		return response.url;
	},

	async selectFreePlan(token: string | null): Promise<void> {
		await postBillingAction("/v1/billing/free-plan", token);
	},
};
