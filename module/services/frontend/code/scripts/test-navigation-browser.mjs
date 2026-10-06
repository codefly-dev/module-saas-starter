// Pure package qualification: no service graph or product authentication.
// Supply skin descriptor paths to exercise real downstream appearances without
// recording consumer identities or descriptors in this repository.
import assert from "node:assert/strict";
import { mkdirSync, mkdtempSync, readFileSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { resolveFrontendAppearance } from "@codefly/saas-plugin-contract";
import { chromium, expect } from "@playwright/test";
import { build } from "esbuild";
import { appearanceStyleProperties } from "../packages/codefly-ui/dist/skin/appearance.js";

const root = resolve(fileURLToPath(new URL("..", import.meta.url)));
const temporary = mkdtempSync(join(tmpdir(), "kit-navigation-"));
const output = resolve(
	root,
	"../../../../.lazybox/artifacts/shared-kit-browser",
);
mkdirSync(output, { recursive: true });
const descriptors = process.argv.slice(2);
const skins = descriptors.length
	? descriptors.map((path) => JSON.parse(readFileSync(path, "utf8")).appearance)
	: [undefined];
const css = readFileSync(
	join(root, "packages/codefly-ui/src/skin/preview.generated.css"),
	"utf8",
);
let browser;
try {
	const bundle = join(temporary, "fixture.js");
	await build({
		stdin: {
			contents: `
 import { createRoot } from "react-dom/client";
 import * as stories from "./packages/codefly-ui/stories/navigation.stories.tsx";
 import { Page, Section } from "./packages/codefly-ui/src/layout/index.ts";
 createRoot(document.getElementById("root")).render(<Page><h1>Navigation and selection</h1>{Object.entries(stories).filter(([key]) => key !== "default").map(([key, story]) => <Section key={key} title={key}><story.render /></Section>)}</Page>);
 `,
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
	browser = await chromium.launch({ headless: true });
	let checks = 0;
	for (const [index, definition] of skins.entries()) {
		const appearance = resolveFrontendAppearance(definition);
		const properties = appearanceStyleProperties(appearance);
		for (const dark of [false, true])
			for (const width of [390, 1280]) {
				const page = await browser.newPage({
					viewport: { width, height: 1000 },
					reducedMotion: "reduce",
				});
				const errors = [];
				page.on("pageerror", (error) => errors.push(error.message));
				await page.setContent(
					'<!doctype html><html lang="en"><head><title>Kit qualification</title></head><body><main id="root" style="padding:1rem"></main></body></html>',
				);
				await page.addStyleTag({ content: css });
				await page.evaluate(
					({ properties, dark }) => {
						for (const [key, value] of Object.entries(properties))
							document.documentElement.style.setProperty(key, String(value));
						document.documentElement.classList.toggle("dark", dark);
					},
					{ properties, dark },
				);
				await page.addScriptTag({ path: bundle });
				const evidence = page.getByRole("button", {
					name: "Evidence",
					exact: true,
				});
				await expect(evidence).toHaveAttribute("aria-expanded", "true");
				await expect(evidence.locator("svg")).toHaveCSS("rotate", "90deg");
				await evidence.focus();
				await page.keyboard.press("Space");
				await expect(evidence).toHaveAttribute("aria-expanded", "false");
				await page.keyboard.press("Enter");
				await expect(evidence).toHaveAttribute("aria-expanded", "true");
				const radio = page.getByRole("radio", { name: "Useful", exact: true });
				await radio.focus();
				await page.keyboard.press("ArrowDown");
				await expect(
					page.getByRole("radio", { name: "Incomplete", exact: true }),
				).toBeChecked();
				await page.keyboard.press("ArrowDown");
				await expect(
					page.getByRole("radio", { name: "Unavailable", exact: true }),
				).not.toBeChecked();
				const tree = page.getByRole("tree", {
					name: "Large collection",
					exact: true,
				});
				await tree.focus();
				await page.keyboard.press("End");
				await expect(
					tree.getByRole("treeitem", { name: "Item 999", exact: true }),
				).toBeVisible();
				assert((await tree.getByRole("treeitem").count()) < 20);
				await page.keyboard.press("Home");
				await expect(
					tree.getByRole("treeitem", { name: "Item 0", exact: true }),
				).toBeVisible();
				await page
					.getByRole("button", { name: "Choose groups", exact: true })
					.click();
				await page
					.getByRole("textbox", { name: "Find a group", exact: true })
					.fill("Group B");
				await page
					.getByRole("checkbox", { name: "Group B", exact: true })
					.click();
				await page.keyboard.press("Escape");
				await expect(
					page.getByRole("button", { name: "Choose groups", exact: true }),
				).toBeFocused();
				await page
					.getByRole("button", { name: "Remove Group B", exact: true })
					.click();
				assert.equal(
					await page
						.getByRole("button", { name: "Remove Group B", exact: true })
						.count(),
					0,
				);
				assert(
					await page.evaluate(
						() => document.documentElement.scrollWidth <= innerWidth,
					),
					"page overflows horizontally",
				);
				assert.deepEqual(errors, []);
				await page.screenshot({
					path: join(
						output,
						`skin-${index + 1}-${dark ? "dark" : "light"}-${width}.png`,
					),
					fullPage: true,
				});
				await page.close();
				checks++;
			}
	}
	console.log(
		`Passed ${checks} Chromium skin/mode/viewport combinations: disclosure, radio, virtualized tree, choices, restored focus and overflow.`,
	);
	console.log(`Screenshots: ${output}`);
} finally {
	await browser?.close();
	rmSync(temporary, { recursive: true, force: true });
}
