import { handlePluginBffRequest } from "../../../../../../../server/plugin-bff";

export const dynamic = "force-dynamic";
export const runtime = "nodejs";

interface RouteContext {
	params: Promise<{ plugin: string; alias: string; path: string[] }>;
}

async function handler(request: Request, context: RouteContext) {
	const params = await context.params;
	return handlePluginBffRequest(request, params);
}

export {
	handler as DELETE,
	handler as GET,
	handler as HEAD,
	handler as OPTIONS,
	handler as PATCH,
	handler as POST,
	handler as PUT,
};
