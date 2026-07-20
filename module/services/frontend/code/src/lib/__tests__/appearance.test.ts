import { resolveFrontendAppearance } from "@codefly/saas-plugin-contract";
import { describe, expect, it } from "vitest";
import {
	appearanceStyleProperties,
	appearanceVariableName,
	readableForeground,
} from "../appearance";

describe("application appearance projection", () => {
	it("projects both resolved palettes into SSR-safe variables", () => {
		const appearance = resolveFrontendAppearance({
			radius: "0.8rem",
			light: { primary: "#112233" },
			dark: { primary: "#ddeeff" },
		});
		const style = appearanceStyleProperties(appearance);

		expect(style["--appearance-radius"]).toBe("0.8rem");
		expect(style["--appearance-light-primary"]).toBe("#112233");
		expect(style["--appearance-dark-primary"]).toBe("#ddeeff");
		expect(style["--appearance-light-card-foreground"]).toBe(
			appearance.light.cardForeground,
		);
		expect(appearanceVariableName("dark", "sidebarPrimaryForeground")).toBe(
			"--appearance-dark-sidebar-primary-foreground",
		);
	});

	it("selects a readable foreground for tenant colors", () => {
		expect(readableForeground("#ffffff")).toBe("#000000");
		expect(readableForeground("#000000")).toBe("#ffffff");
		expect(readableForeground("#111827")).toBe("#ffffff");
	});
});
