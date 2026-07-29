import type { BrowserAnalyticsSink, BrowserCapture } from "./browser";

export class PostHogBrowserSink implements BrowserAnalyticsSink {
	private readonly endpoint: string;
	private readonly apiKey: string;
	private readonly timeoutMs: number;
	private distinctId?: string;

	constructor({
		host,
		apiKey,
		timeoutMs = 5000,
	}: {
		host: string;
		apiKey: string;
		timeoutMs?: number;
	}) {
		const url = new URL(host);
		const localHost =
			url.hostname === "localhost" || url.hostname === "127.0.0.1";
		if (url.protocol !== "https:" && (url.protocol !== "http:" || !localHost)) {
			throw new Error("PostHog analytics host must use HTTPS");
		}
		if (!apiKey) throw new Error("PostHog project API key is required");
		if (!Number.isFinite(timeoutMs) || timeoutMs <= 0 || timeoutMs > 30_000) {
			throw new Error(
				"PostHog analytics timeout must be between 1 and 30000ms",
			);
		}
		this.endpoint = new URL("/batch/", url).toString();
		this.apiKey = apiKey;
		this.timeoutMs = timeoutMs;
	}

	async capture(event: BrowserCapture) {
		const distinctId = event.userId || event.anonymousId;
		await this.send({
			event: event.eventName,
			uuid: event.eventId,
			timestamp: event.occurredAt,
			properties: {
				...event.properties,
				distinct_id: distinctId,
				$session_id: event.context.sessionId,
				$groups: event.organizationId
					? { organization: event.organizationId }
					: undefined,
				schema_version: event.schemaVersion,
				source: "web",
				route: event.context.route,
				release: event.context.release,
				environment: event.context.environment,
				locale: event.context.locale,
				first_touch_source: event.context.firstTouchSource,
				first_touch_campaign: event.context.firstTouchCampaign,
				last_touch_source: event.context.lastTouchSource,
				last_touch_campaign: event.context.lastTouchCampaign,
				...flagProperties(event.context.featureFlags),
			},
		});
	}

	async identify(userId: string, organizationId?: string) {
		this.distinctId = userId;
		await this.send({
			event: "$identify",
			properties: {
				distinct_id: userId,
				$groups: organizationId ? { organization: organizationId } : undefined,
			},
		});
	}

	async alias(anonymousId: string, userId: string) {
		await this.send({
			event: "$create_alias",
			properties: { distinct_id: anonymousId, alias: userId },
		});
	}

	async group(organizationId: string) {
		await this.send({
			event: "$groupidentify",
			properties: {
				distinct_id: this.distinctId ?? organizationId,
				$group_type: "organization",
				$group_key: organizationId,
			},
		});
	}

	reset() {
		this.distinctId = undefined;
	}

	private async send(event: Record<string, unknown>) {
		const response = await fetch(this.endpoint, {
			method: "POST",
			headers: { "Content-Type": "application/json" },
			body: JSON.stringify({ api_key: this.apiKey, batch: [event] }),
			keepalive: true,
			signal: AbortSignal.timeout(this.timeoutMs),
		});
		if (!response.ok) {
			throw new Error(`PostHog analytics returned HTTP ${response.status}`);
		}
	}
}

function flagProperties(flags?: Record<string, string>) {
	return Object.fromEntries(
		Object.entries(flags ?? {}).map(([key, value]) => [
			`feature_flag.${key}`,
			value,
		]),
	);
}
