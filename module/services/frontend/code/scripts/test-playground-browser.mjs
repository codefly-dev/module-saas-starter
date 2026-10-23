// Use the package preview harness: a story bundle, preview.css and Chromium.
import assert from "node:assert/strict";
import { mkdirSync, mkdtempSync, readFileSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { chromium, expect } from "@playwright/test";
import { build } from "esbuild";

const root = resolve(fileURLToPath(new URL("..", import.meta.url)));
const temporary = mkdtempSync(join(tmpdir(), "kit-playground-"));
const output = resolve(root, "../../../../.lazybox/artifacts/playground");
mkdirSync(output, { recursive: true });
let browser;
try {
	const bundle = join(temporary, "fixture.js");
	await build({
		stdin: {
			contents: `import { createRoot } from "react-dom/client";
import { FixtureScenario } from "./packages/codefly-ui/stories/playground.stories.tsx";
createRoot(document.getElementById("root")).render(<FixtureScenario.render />);`,
			resolveDir: root,
			loader: "tsx",
		},
		bundle: true,
		outfile: bundle,
		format: "iife",
		platform: "browser",
		jsx: "automatic",
		define: { "process.env.NODE_ENV": '"production"' },
	});
	const css = readFileSync(
		join(root, "packages/codefly-ui/src/skin/preview.generated.css"),
		"utf8",
	);
	browser = await chromium.launch({ headless: true });
	for (const dark of [false, true])
		for (const width of [390, 1280]) {
			const page = await browser.newPage({
				viewport: { width, height: 1000 },
				reducedMotion: "reduce",
			});
			const errors = [];
			page.on("pageerror", (error) => errors.push(error.message));
			// The same pre-render initialization as test-navigation-browser.mjs.
			await page.setContent(
				`<!doctype html><html lang="en" class="${dark ? "dark" : ""}" style="color-scheme:${dark ? "dark" : "light"}"><head><title>Playground fixture</title></head><body><div id="root"></div></body></html>`,
			);
			await page.addStyleTag({ content: css });
			await page.evaluate(() => {
				window.paletteChanges = [];
				new MutationObserver(() =>
					window.paletteChanges.push(document.documentElement.className),
				).observe(document.documentElement, {
					attributes: true,
					attributeFilter: ["class"],
				});
			});
			await page.addScriptTag({ path: bundle });
			await expect(
				page.getByRole("heading", { name: "Example playground" }),
			).toBeVisible();
			assert.deepEqual(
				await page.evaluate(() => window.paletteChanges),
				[],
				"mount changed the initial palette",
			);
			await expect(
				page.getByRole("button", {
					name: dark ? "Light mode" : "Dark mode",
					exact: true,
				}),
			).toBeVisible();
			await page.getByRole("button", { name: "Record fixture call" }).click();
			if (width === 390)
				await page.getByRole("button", { name: "Toggle Sidebar" }).click();
			await page
				.getByRole("button", { name: "Understand a refusal", exact: true })
				.click();
			if (width === 390) await expect(page.getByRole("dialog")).toBeHidden();
			await expect(
				page.getByText("This fixture principal has read access only.", {
					exact: true,
				}),
			).toBeVisible();
			await page.getByRole("button", { name: "Record fixture call" }).click();
			const refused = page.getByRole("button", {
				name: /Example\.Update\s*Failed/,
			});
			await refused.focus();
			await page.keyboard.press("Enter");
			await expect(refused).toHaveAttribute("aria-expanded", "true");
			await expect(page.getByRole("figure", { name: "Example.Update response", exact: true })).toHaveCount(2);
			await page
				.getByRole("button", {
					name: dark ? "Light mode" : "Dark mode",
					exact: true,
				})
				.click();
			await expect(
				page.getByRole("button", {
					name: dark ? "Dark mode" : "Light mode",
					exact: true,
				}),
			).toBeVisible();
			assert.equal(
				await page.evaluate(() =>
					document.documentElement.classList.contains("dark"),
				),
				!dark,
			);
			await page
				.getByRole("button", {
					name: dark ? "Dark mode" : "Light mode",
					exact: true,
				})
				.click();
			assert(
				await page.evaluate(
					() => document.documentElement.scrollWidth <= innerWidth,
				),
				"horizontal overflow",
			);
			assert.deepEqual(errors, []);
			await page.screenshot({
				path: join(output, `${dark ? "dark" : "light"}-${width}.png`),
				fullPage: true,
			});
			await page.close();
		}
	console.log(
		`Passed light/dark × desktop/mobile: initial palette, navigation, refusals, log expansion, theme toggle and overflow. Screenshots: ${output}`,
	);
} finally {
	await browser?.close();
	rmSync(temporary, { recursive: true, force: true });
}
