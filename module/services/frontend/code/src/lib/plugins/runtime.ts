import { createPluginRuntime } from "@codefly/saas-plugin-react/runtime";

import { getToken } from "@/lib/connect/token-store";

/** Host-private adapter: product code receives only the closure-backed runtime. */
export const hostPluginRuntime = createPluginRuntime({
	getAccessToken: getToken,
});
