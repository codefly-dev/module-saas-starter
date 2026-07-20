import { setupServer } from "msw/node";
import { afterAll, afterEach, beforeAll } from "vitest";
import { resetFixtures } from "./msw/fixtures";
import { handlers } from "./msw/handlers";

export const server = setupServer(...handlers);

beforeAll(() => server.listen({ onUnhandledRequest: "warn" }));
afterEach(() => {
	server.resetHandlers();
	resetFixtures();
});
afterAll(() => server.close());
