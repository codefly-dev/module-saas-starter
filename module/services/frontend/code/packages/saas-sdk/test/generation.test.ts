import { fromBinary } from "@bufbuild/protobuf";
import {
	FileDescriptorSetSchema,
	type DescriptorProto,
} from "@bufbuild/protobuf/wkt";
import { spawnSync } from "node:child_process";
import {
	mkdirSync,
	mkdtempSync,
	readdirSync,
	readFileSync,
	rmSync,
	writeFileSync,
} from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { expect, it } from "vitest";
import { file_saas_accounts_v1_audit } from "../generated/typescript/src/gen/saas/accounts/v1/audit_pb.js";

const sdk = resolve(dirname(fileURLToPath(import.meta.url)), "..");

interface GenerationCall {
	cwd: string;
	args: string[];
}

interface GenerationRun {
	calls: GenerationCall[];
	// The bindings left on disk when the script returned, so a step that removes
	// generated files is observable and not merely inferred from the calls.
	generated: string[];
}

// Drives the package's own npm scripts, not `scripts/generate.mjs` directly:
// the script only generates what an entry point actually invokes, so a manifest
// that never reaches it leaves the second step — the one that re-emits foreign
// descriptors the client step may omit — unreachable from any documented command.
// `seed` stands in for what the two generation steps would have written, since
// the fixture's `codefly` only records how it was called.
function runPackageScript(
	script: string,
	args: string[] = [],
	seed: Record<string, string> = {},
): GenerationRun {
	const temp = mkdtempSync(join(tmpdir(), "audit-sdk-generation-"));
	try {
		const fixtureSdk = join(
			temp,
			"module/services/frontend/code/packages/saas-sdk",
		);
		const accounts = join(temp, "module/services/accounts");
		mkdirSync(join(fixtureSdk, "scripts"), { recursive: true });
		const gen = join(fixtureSdk, "generated/typescript/src/gen");
		mkdirSync(gen, { recursive: true });
		for (const [path, source] of Object.entries(seed)) {
			mkdirSync(dirname(join(gen, path)), { recursive: true });
			writeFileSync(join(gen, path), source);
		}
		mkdirSync(join(accounts, "proto"), { recursive: true });
		writeFileSync(
			join(accounts, "proto/audit.proto"),
			"deliberately incompatible unpublished schema",
		);
		for (const file of ["scripts/generate.mjs", "package.json"]) {
			writeFileSync(join(fixtureSdk, file), readFileSync(join(sdk, file)));
		}
		const log = join(temp, "calls.jsonl");
		writeFileSync(
			join(temp, "codefly"),
			`#!${process.execPath}\nrequire("node:fs").appendFileSync(process.env.GENERATION_CALLS, JSON.stringify({cwd:process.cwd(),args:process.argv.slice(2)})+"\\n");\n`,
			{ mode: 0o755 },
		);
		const npm = process.platform === "win32" ? "npm.cmd" : "npm";
		const argv = ["run", "--silent", script];
		if (args.length > 0) argv.push("--", ...args);
		const result = spawnSync(npm, argv, {
			cwd: fixtureSdk,
			env: {
				...process.env,
				PATH: `${temp}:${process.env.PATH}`,
				GENERATION_CALLS: log,
			},
			encoding: "utf8",
		});
		expect(result.status, result.stderr).toBe(0);
		return {
			calls: readFileSync(log, "utf8")
				.trim()
				.split("\n")
				.map((line) => JSON.parse(line) as GenerationCall),
			generated: readdirSync(gen, { recursive: true })
				.map(String)
				.filter((path) => path.endsWith(".ts"))
				.map((path) => path.replaceAll("\\", "/"))
				.sort(),
		};
	} finally {
		rmSync(temp, { recursive: true, force: true });
	}
}

const flag = (call: GenerationCall, name: string) =>
	call.args[call.args.indexOf(name) + 1];

// The client step decides which foreign descriptors land on disk and has been
// seen omitting them; the sdk template regenerates the whole import closure, so
// it is what keeps `buf/validate` and `google/api` present. A `generate` that
// runs only the client step reintroduces the dangling-import tree by construction.
it("binds both generation steps to the exported contract, ignoring mutable service protos", () => {
	const { calls } = runPackageScript("generate", ["--force"]);
	expect(calls).toHaveLength(2);
	expect(calls[0].args.slice(0, 2)).toEqual(["generate", "client"]);
	expect(calls[1].args.slice(0, 2)).toEqual(["generate", "proto"]);
	expect(flag(calls[1], "--template")).toBe("buf.gen.sdk.yaml");
	expect(calls[0].args).toContain("--force");
	const exported = resolve(
		calls[0].cwd,
		flag(calls[0], "--from").replace("contracts:", ""),
	);
	expect(resolve(calls[1].cwd, flag(calls[1], "--proto"))).toBe(
		join(exported, "accounts/connect/proto"),
	);
});

it("refreshes bindings alone without rewriting the vendored contract or facade", () => {
	const { calls } = runPackageScript("generate:bindings");
	expect(calls).toHaveLength(1);
	expect(calls[0].args.slice(0, 2)).toEqual(["generate", "proto"]);
});

// The selector is this script's own and the CLI has no such option, so a spelling
// it does not recognize must still not reach the step that forwards arguments.
it("keeps the step selector out of the forwarded client arguments", () => {
	const { calls } = runPackageScript("generate", ["--bindings-only=false"]);
	expect(calls).toHaveLength(2);
	expect(calls[0].args.some((arg) => arg.startsWith("--bindings-only"))).toBe(
		false,
	);
});

function messageContract(message: DescriptorProto): unknown {
	return {
		name: message.name,
		fields: message.field.map(
			({
				name,
				number,
				type,
				typeName,
				label,
				oneofIndex,
				proto3Optional,
			}) => ({
				name,
				number,
				type,
				typeName,
				label,
				oneofIndex,
				proto3Optional,
			}),
		),
		nested: message.nestedType.map(messageContract),
	};
}

it("generated audit field contracts equal the exported descriptor, not merely its digest label", () => {
	const exported = fromBinary(
		FileDescriptorSetSchema,
		readFileSync(
			resolve(
				sdk,
				"../../../../../contracts/api/accounts/connect/contract.binpb",
			),
		),
	);
	const audit = exported.file.find(
		(file) => file.name === "saas/accounts/v1/audit.proto",
	);
	expect(audit).toBeDefined();
	expect(
		file_saas_accounts_v1_audit.proto.messageType.map(messageContract),
	).toEqual(audit?.messageType.map(messageContract));
});

// `buf.gen.sdk.yaml` declares no `clean`, so the backstop supersedes the client
// step's bindings without removing the ones it no longer emits: the pinned plugin
// resolves well-known types to `@bufbuild/protobuf/wkt`, so a `google/protobuf`
// descriptor the client step wrote survives the run stale and imported by
// nothing — which the composition gate rejects. Regenerating has to reproduce the
// committed tree, or the documented command cannot be followed without a red gate.
// The transitive case is the reason this prunes to a fixed point rather than once:
// `duration_pb` is reachable only from a descriptor that is itself unreachable.
it("drops third-party descriptors the regenerated bindings no longer import", () => {
	const { generated } = runPackageScript("generate", ["--force"], {
		"saas/accounts/v1/audit_pb.ts":
			'import { file_buf_validate } from "../../../buf/validate/validate_pb";\n',
		"buf/validate/validate_pb.ts": "export const file_buf_validate = 1;\n",
		"google/protobuf/descriptor_pb.ts":
			'import { file_google_protobuf_duration } from "./duration_pb";\n',
		"google/protobuf/duration_pb.ts":
			"export const file_google_protobuf_duration = 1;\n",
	});
	expect(generated).toEqual([
		"buf/validate/validate_pb.ts",
		"saas/accounts/v1/audit_pb.ts",
	]);
});
