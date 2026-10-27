import type { DatasourceProviderView } from "../src/datasources/types.js";

export const repositoryProvider: DatasourceProviderView = {
	provider: 1,
	connector: "github",
	displayName: "GitHub",
	description: "Example repository provider",
	supportsOrganizationAppSetup: true,
	acceptsNewSources: true,
	gap: "",
	supportsWebhook: true,
	credentialModes: ["static_secret", "none", "org_app"],
	fields: [
		{
			key: "repo",
			displayName: "Repository",
			help: "",
			required: true,
			format: "text",
		},
		{
			key: "paths",
			displayName: "Paths",
			help: "",
			required: false,
			format: "lines",
		},
		{
			key: "file_extensions",
			displayName: "File types",
			help: "",
			required: false,
			format: "lines",
		},
		{
			key: "branch",
			displayName: "Branch",
			help: "",
			required: false,
			format: "text",
		},
	],
};

// Deliberately not a provider known to the panel. Its descriptor owns the fields.
export const exampleProvider: DatasourceProviderView = {
	provider: 42,
	connector: "example",
	displayName: "Example archive",
	description: "An example source with its own fields.",
	acceptsNewSources: true,
	gap: "",
	supportsWebhook: false,
	credentialModes: ["static_secret"],
	fields: [
		{
			key: "prefix",
			displayName: "Key prefix",
			help: "Limit the source to these keys.",
			required: true,
			format: "text",
		},
		{
			key: "include_patterns",
			displayName: "Include patterns",
			help: "",
			required: false,
			format: "lines",
		},
		{
			key: "exclude_patterns",
			displayName: "Exclude patterns",
			help: "",
			required: false,
			format: "lines",
		},
	],
};
export const refusedProvider: DatasourceProviderView = {
	...exampleProvider,
	connector: "example-refused",
	displayName: "Example restricted archive",
	acceptsNewSources: false,
	gap: "Example deployment cannot enforce item readers; ask its operator.",
};
