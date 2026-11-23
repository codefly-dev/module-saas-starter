import { createClient } from "@connectrpc/connect";
import { SessionService } from "@/gen/host/session/v1/session_pb";
import { apiTransport } from "@/lib/connect/transport";

const sessions = createClient(SessionService, apiTransport);

/**
 * `hostSession` for a test's stand-in of the auth provider: the host's
 * `Session.Current`, asked each time, so what a test's host answers is what
 * the page under test reads. The provider's own sharing of one read is held by
 * its tests (lib/__tests__/session-authority.test.tsx).
 */
export const askHostForSession = () => sessions.current({});
