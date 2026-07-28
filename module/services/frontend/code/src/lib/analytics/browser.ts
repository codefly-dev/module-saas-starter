export type AnalyticsPurpose = "product" | "marketing" | "replay";
export type ConsentState = "unknown" | "granted" | "denied" | "withdrawn";

export type BrowserEventName =
	| "landing_viewed"
	| "signup_started"
	| "invite_opened"
	| "onboarding_started"
	| "onboarding_step_viewed"
	| "core_action_started"
	| "feedback_opened"
	| "survey_shown"
	| "notification_opened"
	| "notification_clicked";

type BrowserEventDefinition = {
	purpose: AnalyticsPurpose;
	properties: Readonly<Record<string, BrowserPropertyType>>;
};

type BrowserPropertyType = "string" | "number" | "boolean";

export const browserEventRegistry = {
	landing_viewed: {
		purpose: "marketing",
		properties: { page_kind: "string" },
	},
	signup_started: {
		purpose: "product",
		properties: { flow_version: "string", provider: "string" },
	},
	invite_opened: {
		purpose: "product",
		properties: { flow_version: "string" },
	},
	onboarding_started: {
		purpose: "product",
		properties: { flow_version: "string", variant: "string" },
	},
	onboarding_step_viewed: {
		purpose: "product",
		properties: {
			step_name: "string",
			flow_version: "string",
			variant: "string",
		},
	},
	core_action_started: {
		purpose: "product",
		properties: { action: "string", definition_version: "string" },
	},
	feedback_opened: {
		purpose: "product",
		properties: { surface: "string" },
	},
	survey_shown: {
		purpose: "product",
		properties: { survey_key: "string", variant: "string" },
	},
	notification_opened: {
		purpose: "product",
		properties: { channel: "string", template_key: "string" },
	},
	notification_clicked: {
		purpose: "product",
		properties: {
			channel: "string",
			template_key: "string",
			action: "string",
		},
	},
} as const satisfies Record<BrowserEventName, BrowserEventDefinition>;

export type AnalyticsContext = {
	route?: string;
	release?: string;
	environment?: string;
	locale?: string;
	sessionId?: string;
	firstTouchSource?: string;
	firstTouchCampaign?: string;
	lastTouchSource?: string;
	lastTouchCampaign?: string;
	featureFlags?: Record<string, string>;
};

export type BrowserCapture = {
	eventId: string;
	eventName: BrowserEventName;
	schemaVersion: 1;
	occurredAt: string;
	anonymousId: string;
	userId?: string;
	organizationId?: string;
	purpose: AnalyticsPurpose;
	properties: Record<string, string | number | boolean | null>;
	context: AnalyticsContext;
};

export interface BrowserAnalyticsSink {
	capture(event: BrowserCapture): Promise<void>;
	identify(userId: string, organizationId?: string): Promise<void>;
	alias(anonymousId: string, userId: string): Promise<void>;
	group(organizationId: string): Promise<void>;
	reset(): void;
}

export interface AnalyticsStorage {
	getItem(key: string): string | null;
	setItem(key: string, value: string): void;
	removeItem(key: string): void;
}

export class BrowserAnalytics {
	private readonly sink: BrowserAnalyticsSink;
	private readonly storage: AnalyticsStorage;
	private readonly context: AnalyticsContext;
	private consent: Record<AnalyticsPurpose, ConsentState>;
	private userId?: string;
	private organizationId?: string;
	private anonymousId: string;

	constructor({
		sink,
		storage,
		consent,
		context = {},
	}: {
		sink: BrowserAnalyticsSink;
		storage: AnalyticsStorage;
		consent?: Partial<Record<AnalyticsPurpose, ConsentState>>;
		context?: AnalyticsContext;
	}) {
		this.sink = sink;
		this.storage = storage;
		validateContext(context);
		this.context = {
			...context,
			featureFlags: context.featureFlags
				? { ...context.featureFlags }
				: undefined,
		};
		this.consent = {
			product: consent?.product ?? "unknown",
			marketing: consent?.marketing ?? "unknown",
			replay: consent?.replay ?? "unknown",
		};
		this.anonymousId = getOrCreateAnonymousID(storage);
	}

	setConsent(purpose: AnalyticsPurpose, state: ConsentState) {
		this.consent[purpose] = state;
		if (state === "denied" || state === "withdrawn") {
			this.sink.reset();
		}
	}

	async track(
		eventName: BrowserEventName,
		properties: BrowserCapture["properties"] = {},
	): Promise<boolean> {
		const definition = browserEventRegistry[eventName];
		if (this.consent[definition.purpose] !== "granted") return false;
		validateProperties(eventName, definition, properties);
		await this.sink.capture({
			eventId: crypto.randomUUID(),
			eventName,
			schemaVersion: 1,
			occurredAt: new Date().toISOString(),
			anonymousId: this.anonymousId,
			userId: this.userId,
			organizationId: this.organizationId,
			purpose: definition.purpose,
			properties: { ...properties },
			context: this.context,
		});
		return true;
	}

	async identify(userId: string, organizationId?: string) {
		if (this.consent.product !== "granted") return false;
		if (!userId) throw new Error("Analytics user ID is required");
		const aliasedUser = this.storage.getItem(aliasedUserKey);
		if (aliasedUser && aliasedUser !== userId) {
			this.rotateAnonymousID();
		}
		if (this.anonymousId !== userId && aliasedUser !== userId) {
			await this.sink.alias(this.anonymousId, userId);
			this.storage.setItem(aliasedUserKey, userId);
		}
		this.userId = userId;
		this.organizationId = organizationId;
		await this.sink.identify(userId, organizationId);
		if (organizationId) await this.sink.group(organizationId);
		return true;
	}

	reset() {
		this.userId = undefined;
		this.organizationId = undefined;
		this.sink.reset();
		this.rotateAnonymousID();
	}

	private rotateAnonymousID() {
		this.storage.removeItem(anonymousIDKey);
		this.storage.removeItem(aliasedUserKey);
		this.anonymousId = getOrCreateAnonymousID(this.storage);
	}
}

export class NoopBrowserAnalyticsSink implements BrowserAnalyticsSink {
	async capture() {}
	async identify() {}
	async alias() {}
	async group() {}
	reset() {}
}

const anonymousIDKey = "saas.analytics.anonymous_id";
const aliasedUserKey = "saas.analytics.aliased_user";

export function getOrCreateAnonymousID(storage: AnalyticsStorage): string {
	const existing = storage.getItem(anonymousIDKey);
	if (existing) return existing;
	const anonymousID = crypto.randomUUID();
	storage.setItem(anonymousIDKey, anonymousID);
	return anonymousID;
}

function validateProperties(
	eventName: BrowserEventName,
	definition: BrowserEventDefinition,
	properties: BrowserCapture["properties"],
) {
	for (const [key, value] of Object.entries(properties)) {
		const expectedType = definition.properties[key];
		if (!expectedType) {
			throw new Error(
				`Analytics property ${key} is not registered for ${eventName}`,
			);
		}
		if (value === null || typeof value !== expectedType) {
			throw new Error(`Analytics property ${key} must be a ${expectedType}`);
		}
		if (typeof value === "string" && value.length > 256) {
			throw new Error(`Analytics property ${key} exceeds 256 characters`);
		}
		if (typeof value === "number" && !Number.isFinite(value)) {
			throw new Error(`Analytics property ${key} must be finite`);
		}
		if (typeof value === "string" && containsSensitiveValue(value)) {
			throw new Error(`Analytics property ${key} contains forbidden data`);
		}
	}
}

function validateContext(context: AnalyticsContext) {
	if (context.route?.match(/[?#]/)) {
		throw new Error("Analytics route must not contain a query or fragment");
	}
	const limits: [keyof AnalyticsContext, number][] = [
		["route", 256],
		["release", 128],
		["environment", 64],
		["locale", 32],
		["sessionId", 128],
		["firstTouchSource", 128],
		["firstTouchCampaign", 128],
		["lastTouchSource", 128],
		["lastTouchCampaign", 128],
	];
	for (const [key, limit] of limits) {
		const value = context[key];
		if (typeof value !== "string") continue;
		if (value.length > limit) {
			throw new Error(`Analytics context ${key} exceeds ${limit} characters`);
		}
		if (containsSensitiveValue(value)) {
			throw new Error(`Analytics context ${key} contains forbidden data`);
		}
	}
	const flags = Object.entries(context.featureFlags ?? {});
	if (flags.length > 32) {
		throw new Error("Analytics context contains more than 32 feature flags");
	}
	for (const [key, value] of flags) {
		if (!/^[a-z][a-z0-9_.-]{0,63}$/.test(key)) {
			throw new Error(`Analytics feature flag ${key} is not canonical`);
		}
		if (value.length > 128 || containsSensitiveValue(value)) {
			throw new Error(`Analytics feature flag ${key} contains forbidden data`);
		}
	}
}

function containsSensitiveValue(value: string): boolean {
	return (
		/(?:^|[^a-z0-9.!#$%&'*+/=?^_`{|}~-])[a-z0-9.!#$%&'*+/=?^_`{|}~-]+@[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?(?:\.[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?)+(?:$|[^a-z0-9.-])/i.test(
			value,
		) ||
		/(?:bearer\s+[a-z0-9._~+/=-]{12,}|(?:sk|pk|rk)_(?:live|test)_[a-z0-9]{12,})/i.test(
			value,
		)
	);
}
