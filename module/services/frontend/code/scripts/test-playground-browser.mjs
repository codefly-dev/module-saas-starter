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
import { Button, Table, TableBody, TableRow, TableCell } from "./packages/codefly-ui/src/layout/index.ts";
import { PlaygroundShell, ScenarioSteps, Step, OperationLog } from "./packages/codefly-ui/src/playground/index.tsx";
import { ThemeProvider, useTheme } from "./src/lib/theme-provider.tsx";
function Fixture() {
 const { resolvedTheme, setTheme } = useTheme();
 return <><output hidden data-theme-owner={resolvedTheme} />
  <FixtureScenario.render actions={<Button variant="ghost" onClick={() => setTheme(resolvedTheme === "dark" ? "light" : "dark")}>
   {resolvedTheme === "dark" ? "Light mode" : "Dark mode"}
  </Button>} /></>;
}
const initialTheme = document.documentElement.classList.contains("dark") ? "dark" : "light";
const root = createRoot(document.getElementById("root"));
root.render(<ThemeProvider defaultTheme={initialTheme}><Fixture /></ThemeProvider>);
const operation = "Example." + "Operation".repeat(8);
const refusal = "Permission denied: " + "reason".repeat(100);
window.renderOverflowFixture = () => root.render(
 <PlaygroundShell title={operation} moduleName={operation} composition={operation}
  scenarios={Array.from({length: 30}, (_,i) => ({id: String(i), label: operation + i}))}
  active="0" onSelect={() => {}}>
  <ScenarioSteps><Step title={operation} operation={operation} request={{value: "x".repeat(500)}}
   response={{code: "permission_denied"}} state="failed" refusal={refusal}>
   <Table><TableBody><TableRow>{Array.from({length: 8},(_,i) => <TableCell key={i}>{operation}</TableCell>)}</TableRow></TableBody></Table>
  </Step></ScenarioSteps>
  <OperationLog entries={[{id:"1", operation, request:{}, state:"failed", refusal}]} />
 </PlaygroundShell>);`,
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
	for (const dark of [false, true]) {
		const page = await browser.newPage({ viewport: { width: 390, height: 1000 }, reducedMotion: "reduce" });
		page.setDefaultTimeout(10000);
		await page.setContent(`<html class="${dark ? "dark" : ""}"><body><div id="root"></div></body></html>`);
		await page.addStyleTag({ content: css });
		await page.addScriptTag({ path: bundle });
		await page.evaluate(() => window.renderOverflowFixture());
		const operation = "Example." + "Operation".repeat(8);
		await page.getByRole("heading", { name: operation, level: 1 }).waitFor();
		const fits = () => page.evaluate(() => document.documentElement.scrollWidth <= innerWidth);
		assert(await fits(), "long step/title/footer overflow at 390px");
		await expect(page.getByText("Permission denied: " + "reason".repeat(100), { exact: true })).toBeVisible();
		const table = page.locator('[data-slot="table-container"]');
		assert(await table.evaluate(el => el.scrollWidth > el.clientWidth), "wide table should scroll locally");
		await table.evaluate(el => { el.scrollLeft = el.scrollWidth; });
		assert(await table.evaluate(el => el.scrollLeft > 0), "table cannot scroll to its last cell");
		const disclosure = page.getByRole("button", { name: /Operation.*Failed/ });
		await disclosure.focus();
		await page.keyboard.press("Enter");
		await expect(disclosure).toHaveAttribute("aria-expanded", "true");
		assert(await fits(), "expanded long operation overflows at 390px");
		await page.getByRole("button", { name: "Toggle Sidebar" }).click();
		const last = page.getByRole("button", { name: operation + "29", exact: true });
		for (let index = 0; index < 29; index++) await page.keyboard.press("Tab");
		await expect(last).toBeFocused();
		await expect(last).toBeInViewport();
		assert(await fits(), "long navigation overflows at 390px");
		await page.keyboard.press("Enter");
		await expect(page.getByRole("dialog")).toBeHidden();
		await expect(page.getByRole("button", { name: "Toggle Sidebar" })).toBeFocused();
		await page.close();
	}
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
			await expect(page.locator("[data-theme-owner]")).toHaveAttribute("data-theme-owner", dark ? "light" : "dark");
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
		`Passed light/dark × desktop/mobile: initial palette, provider-owned theme, navigation, refusals, log expansion and overflow (including long evidence and wide tables at 390px). Screenshots: ${output}`,
	);
} finally {
	await browser?.close();
	rmSync(temporary, { recursive: true, force: true });
}
