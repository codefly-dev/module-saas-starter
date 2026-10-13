import { createPluginRuntime } from "@codefly-dev/ui/plugin-host/runtime";

import { getToken } from "@/lib/connect/token-store";
import frontendConfig from "../../../frontend.config";

/**
 * Host-private adapter: product code receives only the closure-backed runtime.
 *
 * The installed requirements go with it so the browser transport applies the
 * same admission rule the BFF does — in particular, which Connect services an
 * alias may reach. They carry no address or credential, only what each plugin
 * declared, and the host remains the authority either way.
 */
export const hostPluginRuntime = createPluginRuntime({
	getAccessToken: getToken,
	services: frontendConfig.services,
});
