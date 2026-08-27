import { createPluginRuntime } from "@codefly/ui/plugin-host/runtime";

import { getToken } from "@/lib/connect/token-store";

/** Host-private adapter: product code receives only the closure-backed runtime. */
export const hostPluginRuntime = createPluginRuntime({
	getAccessToken: getToken,
});
