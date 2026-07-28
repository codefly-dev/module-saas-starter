import { afterEach, describe, expect, it, vi } from "vitest";
import type { BrowserCapture } from "./browser";
import { PostHogBrowserSink } from "./posthog-browser";

afterEach(() => {
	vi.unstubAllGlobals();
});

describe("PostHog browser analytics adapter", () => {
	it("maps the canonical event without importing a vendor SDK", async () => {
		const fetchMock = vi.fn(
			async (input: RequestInfo | URL, request?: RequestInit) => {
				void input;
				void request;
				return { ok: true, status: 200 } as Response;
			},
		);
		vi.stubGlobal("fetch", fetchMock);
		const sink = new PostHogBrowserSink({
			host: "http://localhost:8000",
			apiKey: "phc_test",
		});
		const event: BrowserCapture = {
			eventId: "8f7ed52b-4724-4dc5-aa85-2a43311818cc",
			eventName: "core_action_started",
			schemaVersion: 1,
			occurredAt: "2026-07-28T10:00:00.000Z",
			anonymousId: "19d03f5c-4a6c-4918-bf5b-646611ad28c7",
			userId: "30a99cc3-f5b1-445b-985c-a883064992ce",
			organizationId: "a3c6a3af-c34b-4382-b189-a59c6afe3801",
			purpose: "product",
			properties: { action: "publish", definition_version: "v1" },
			context: {
				route: "/projects",
				sessionId: "6eb16c98-f4b9-4eaf-93a2-610ee2d6bf7e",
				featureFlags: { editor: "treatment" },
			},
		};

		await sink.capture(event);

		expect(fetchMock).toHaveBeenCalledOnce();
		const [endpoint, request] = fetchMock.mock.calls[0] ?? [];
		expect(endpoint).toBe("http://localhost:8000/batch/");
		expect(request?.signal).toBeInstanceOf(AbortSignal);
		const body = JSON.parse(request?.body as string);
		expect(body.api_key).toBe("phc_test");
		expect(body.batch[0].uuid).toBe(event.eventId);
		expect(body.batch[0].properties.distinct_id).toBe(event.userId);
		expect(body.batch[0].properties.$groups.organization).toBe(
			event.organizationId,
		);
		expect(body.batch[0].properties["feature_flag.editor"]).toBe("treatment");
	});

	it("requires HTTPS outside local development and a bounded timeout", () => {
		expect(
			() =>
				new PostHogBrowserSink({
					host: "ftp://localhost",
					apiKey: "phc_test",
				}),
		).toThrow("HTTPS");
		expect(
			() =>
				new PostHogBrowserSink({
					host: "http://analytics.example",
					apiKey: "phc_test",
				}),
		).toThrow("HTTPS");
		expect(
			() =>
				new PostHogBrowserSink({
					host: "https://analytics.example",
					apiKey: "phc_test",
					timeoutMs: 0,
				}),
		).toThrow("timeout");
	});

	it("surfaces provider failures for the caller to retry or report", async () => {
		vi.stubGlobal(
			"fetch",
			vi.fn(async () => ({ ok: false, status: 503 })),
		);
		const sink = new PostHogBrowserSink({
			host: "https://analytics.example",
			apiKey: "phc_test",
		});
		await expect(sink.group("org-1")).rejects.toThrow("HTTP 503");
	});
});
