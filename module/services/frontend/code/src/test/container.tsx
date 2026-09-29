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
	// The client comes back alongside the render result so a test can drive a
	// SECOND read of the same query — the success-then-refused sequence, which is
	// the one that proves a denial is not hidden behind rows already on screen.
	// A cold denied read cannot show that, because there is nothing retained.
	return {
		...render(<QueryClientProvider client={client}>{ui}</QueryClientProvider>),
		client,
	};
}
