import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render } from "@testing-library/react";

// Connect RPC endpoint URL under the happy-dom origin. The shared apiTransport
// uses baseUrl "/", so MSW intercepts requests at this same-origin shape.
export function rpc(service: string, method: string): string {
	return `http://localhost:3000/saas.accounts.v1.${service}/${method}`;
}

// Render an admin feature container with the single provider its data hooks
// need. retry is off so a rejected ancillary query surfaces at once rather
// than stalling the test.
export function renderInApp(ui: React.ReactElement) {
	const client = new QueryClient({
		defaultOptions: { queries: { retry: false } },
	});
	return render(
		<QueryClientProvider client={client}>{ui}</QueryClientProvider>,
	);
}
