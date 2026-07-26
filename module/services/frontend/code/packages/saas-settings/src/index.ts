export interface SettingsLookup<T> {
	readonly value: T | undefined;
	readonly present: boolean;
}

export interface SettingsField<
	Settings,
	Patch,
	Value,
	Path extends string = string,
> {
	readonly path: Path;
	readonly default: Value;
	lookup(settings: Settings | undefined): SettingsLookup<Value>;
	has(settings: Settings | undefined): boolean;
	get(settings: Settings | undefined): Value;
	patch(value: Value): Patch;
}

// defineSettingsField binds one generated-protobuf leaf to its typed default,
// patch constructor, and stable protobuf field-mask path. It deliberately
// depends on no concrete Settings schema: SaaS products generate their actual
// protobuf and use this helper unchanged.
export function defineSettingsField<
	Settings,
	Patch,
	Value,
	const Path extends string,
>(
	path: Path,
	defaultValue: Value,
	getStored: (settings: Settings | undefined) => Value | null | undefined,
	patch: (value: Value) => Patch,
): SettingsField<Settings, Patch, Value, Path> {
	const lookup = (settings: Settings | undefined): SettingsLookup<Value> => {
		const value = getStored(settings);
		if (value === undefined || value === null) {
			return { value: undefined, present: false };
		}
		return { value, present: true };
	};
	return {
		path,
		default: defaultValue,
		lookup,
		has(settings): boolean {
			return lookup(settings).present;
		},
		get(settings): Value {
			const stored = lookup(settings);
			return stored.present ? (stored.value as Value) : defaultValue;
		},
		patch,
	};
}

// createSettingsFieldFactory binds the generated root settings and patch
// message types once. Concrete product catalogs then infer each leaf type from
// its selector and patch constructor without changing this generic package.
export function createSettingsFieldFactory<Settings, Patch>() {
	return function defineProductSettingsField<Value, const Path extends string>(
		path: Path,
		defaultValue: Value,
		getStored: (settings: Settings | undefined) => Value | null | undefined,
		patch: (value: Value) => Patch,
	): SettingsField<Settings, Patch, Value, Path> {
		return defineSettingsField(path, defaultValue, getStored, patch);
	};
}

export interface SettingsUpdate<Patch, Path extends string> {
	readonly patch: Patch;
	readonly clearMask?: { readonly paths: Path[] };
}

// createSettingsUpdate keeps reset semantics out of JSON. The concrete
// generated request wrapper supplies its typed empty/partial patch.
export function createSettingsUpdate<Patch, const Path extends string>(
	patch: Patch,
	clearPaths: readonly Path[] = [],
): SettingsUpdate<Patch, Path> {
	return {
		patch,
		clearMask:
			clearPaths.length > 0 ? { paths: Array.from(clearPaths) } : undefined,
	};
}

type SettingsPatchRecord = Record<string, unknown>;

function isPlainRecord(value: unknown): value is SettingsPatchRecord {
	if (typeof value !== "object" || value === null || Array.isArray(value)) {
		return false;
	}
	const prototype = Object.getPrototypeOf(value);
	return prototype === Object.prototype || prototype === null;
}

function assertSafeKey(key: string): void {
	if (key === "__proto__" || key === "prototype" || key === "constructor") {
		throw new Error(`unsafe settings patch key ${JSON.stringify(key)}`);
	}
}

function mergePatchValue(
	current: unknown,
	incoming: unknown,
	path: string,
): unknown {
	if (incoming === null) {
		throw new Error(
			`settings patch ${path || "<root>"} is null; use a clear-mask path`,
		);
	}
	if (incoming === undefined) {
		return current;
	}
	if (isPlainRecord(incoming)) {
		const result: SettingsPatchRecord = isPlainRecord(current)
			? { ...current }
			: {};
		for (const key of Object.keys(incoming)) {
			assertSafeKey(key);
			const childPath = path === "" ? key : `${path}.${key}`;
			const value = mergePatchValue(result[key], incoming[key], childPath);
			if (value !== undefined) {
				result[key] = value;
			}
		}
		return result;
	}
	if (Array.isArray(incoming)) {
		return incoming.slice();
	}
	return incoming;
}

// mergeSettingsPatches recursively combines independently constructed
// generated-message init patches. Nested siblings survive; explicit false,
// empty string, and zero replace prior values; arrays replace instead of
// concatenating; undefined is absence; null is rejected.
export function mergeSettingsPatches<Patch extends SettingsPatchRecord>(
	...patches: readonly Patch[]
): Patch {
	let merged: SettingsPatchRecord = {};
	for (const patch of patches) {
		merged = mergePatchValue(merged, patch, "") as SettingsPatchRecord;
	}
	return merged as Patch;
}
