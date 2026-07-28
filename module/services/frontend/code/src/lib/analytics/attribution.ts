import type { AnalyticsContext, AnalyticsStorage } from "./browser";

const firstTouchKey = "saas.analytics.first_touch";
const lastTouchKey = "saas.analytics.last_touch";

type Touch = {
	source?: string;
	campaign?: string;
};

export function captureAttribution(
	storage: AnalyticsStorage,
	location: URL,
	referrer?: string,
): Pick<
	AnalyticsContext,
	| "firstTouchSource"
	| "firstTouchCampaign"
	| "lastTouchSource"
	| "lastTouchCampaign"
> {
	const touch = {
		source:
			bounded(location.searchParams.get("utm_source")) ??
			referrerHostname(referrer),
		campaign: bounded(location.searchParams.get("utm_campaign")),
	};
	const first = parseTouch(storage.getItem(firstTouchKey)) ?? touch;
	storage.setItem(firstTouchKey, JSON.stringify(first));
	storage.setItem(lastTouchKey, JSON.stringify(touch));
	return {
		firstTouchSource: first.source,
		firstTouchCampaign: first.campaign,
		lastTouchSource: touch.source,
		lastTouchCampaign: touch.campaign,
	};
}

function parseTouch(value: string | null): Touch | undefined {
	if (!value) return undefined;
	try {
		const parsed = JSON.parse(value) as Touch;
		return {
			source: bounded(parsed.source),
			campaign: bounded(parsed.campaign),
		};
	} catch {
		return undefined;
	}
}

function referrerHostname(referrer?: string) {
	if (!referrer) return undefined;
	try {
		return bounded(new URL(referrer).hostname);
	} catch {
		return undefined;
	}
}

function bounded(value: string | null | undefined) {
	const normalized = value?.trim();
	return normalized ? normalized.slice(0, 128) : undefined;
}
