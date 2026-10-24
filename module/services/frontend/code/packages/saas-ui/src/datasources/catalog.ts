import {
	type DescField,
	fromJson,
	type JsonObject,
	type MessageShape,
	ScalarType,
} from "@bufbuild/protobuf";
import { DatasourceService } from "@codefly-dev/saas-sdk";
import type {
	ConnectSourceInput,
	DatasourceConnectField,
	DatasourceProviderView,
} from "./types.js";

const request = DatasourceService.method.addSource.input;
type Provider = MessageShape<
	typeof DatasourceService.method.getDatasourceCatalog.output
>["providers"][number];

// The provider's registry key names its config oneof. The SDK, not a second
// provider table, owns field types and wire encoding.
function configField(connector: string) {
	return request.oneofs
		.find((oneof) => oneof.name === "config")
		?.fields.find((field) => field.name === connector);
}
function format(field?: DescField): DatasourceConnectField["format"] {
	if (field?.fieldKind === "scalar" && field.scalar === ScalarType.STRING)
		return "text";
	if (
		field?.fieldKind === "list" &&
		field.listKind === "scalar" &&
		field.scalar === ScalarType.STRING
	)
		return "lines";
	return "json";
}
export function catalogProvider(provider: Provider): DatasourceProviderView {
	const config = configField(provider.connector);
	const fields = config?.fieldKind === "message" ? config.message.fields : [];
	const modes =
		DatasourceService.method.getDatasourceCatalog.output.fields.find(
			(field) => field.name === "providers",
		);
	const mode =
		modes?.fieldKind === "list" && modes.listKind === "message"
			? modes.message.fields.find((field) => field.name === "credential_modes")
			: undefined;
	const credentialModes = provider.credentialModes.map((value) =>
		mode?.fieldKind === "list" && mode.listKind === "enum"
			? (mode.enum.values.find((entry) => entry.number === value)?.name ?? "")
					.replace("DATASOURCE_CREDENTIAL_MODE_", "")
					.toLowerCase()
			: "",
	);
	return {
		provider: provider.provider,
		connector: provider.connector,
		displayName: provider.displayName,
		description: provider.description,
		acceptsNewSources: provider.acceptsNewSources,
		gap: provider.conformanceGap,
		supportsWebhook: provider.supportsWebhook,
		// A credential mode alone does not promise an installation workflow.
		// Require both SDK operations for the catalog connector as well.
		supportsOrganizationAppSetup:
			credentialModes.includes("org_app") &&
			["begin", "complete"].every((leg) =>
				DatasourceService.methods.some(
					(method) =>
						method.name.toLowerCase() ===
						`${leg}${provider.connector.toLowerCase()}appsetup`,
				),
			),
		credentialModes,
		fields: provider.configFields.map((field) => ({
			key: field.key,
			displayName: field.displayName,
			help: field.help,
			required: field.required,
			format: format(fields.find((entry) => entry.name === field.key)),
		})),
		...(!config ||
		provider.configFields.some(
			(field) => !fields.some((entry) => entry.name === field.key),
		)
			? {
					unsupportedReason:
						"This host's provider fields require a newer client kit.",
				}
			: {}),
	};
}

export function sourceRequest(
	input: ConnectSourceInput,
): MessageShape<typeof DatasourceService.method.addSource.input> {
	if (!input.provider.acceptsNewSources)
		throw new Error("Provider does not accept new sources.");
	if (input.provider.unsupportedReason)
		throw new Error(input.provider.unsupportedReason);
	const config = configField(input.provider.connector);
	if (config?.fieldKind !== "message")
		throw new Error("Unsupported provider configuration.");
	const values: JsonObject = {};
	for (const field of input.provider.fields) {
		const raw = input.config[field.key] ?? "";
		if (field.required && !raw.trim())
			throw new Error(`${field.displayName} is required.`);
		if (!raw.trim()) continue;
		const wire = config.message.fields.find(
			(entry) => entry.name === field.key,
		);
		if (!wire) throw new Error("Unsupported provider field.");
		try {
			values[wire.jsonName] =
				format(wire) === "text"
					? raw
					: format(wire) === "lines"
						? raw
								.split(/\r?\n/)
								.map((line) => line.trim())
								.filter(Boolean)
						: JSON.parse(raw);
		} catch {
			throw new Error(`${field.displayName} must contain valid JSON.`);
		}
	}
	const credential = input.credential;
	// fromJson validates and produces the generated typed request, including the
	// selected oneof. Unknown fields are never silently dropped.
	return fromJson(request, {
		orgId: input.orgId,
		provider: input.provider.provider,
		[config.jsonName]: values,
		...(input.boundaryNodeId
			? { boundaryNodeId: input.boundaryNodeId }
			: { collectionLabel: input.targetCollection }),
		...(credential.kind === "static_secret"
			? {
					credential: credential.secret,
					webhookSecret: credential.webhookSecret ?? "",
					oauth2ClientSecret: credential.oauth2ClientSecret ?? "",
				}
			: {}),
	});
}
