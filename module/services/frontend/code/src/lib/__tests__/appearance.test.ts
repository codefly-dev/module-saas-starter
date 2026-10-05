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
		// Structural tokens fall back to the neutral defaults when omitted.
		expect(style["--appearance-spacing"]).toBe("0.25rem");
		expect(style["--appearance-font-size-base"]).toBe("1rem");
		expect(style["--appearance-sidebar-width"]).toBe("16rem");
		expect(style["--appearance-sidebar-width-icon"]).toBe("3rem");
		expect(style["--appearance-border-width"]).toBe("1px");
		expect(style["--appearance-shadow-strength"]).toBe("1");
		expect(style["--appearance-light-card-foreground"]).toBe(
			appearance.light.cardForeground,
		);
		expect(appearanceVariableName("dark", "sidebarPrimaryForeground")).toBe(
			"--appearance-dark-sidebar-primary-foreground",
		);
	});

	it("projects overridden structural tokens", () => {
		const style = appearanceStyleProperties(
			resolveFrontendAppearance({
				spacing: "0.2rem",
				fontSizeBase: "0.9375rem",
				sidebarWidth: "18rem",
				sidebarWidthIcon: "3.5rem",
				borderWidth: "2px",
				shadowStrength: "1.5",
			}),
		);
		expect(style["--appearance-spacing"]).toBe("0.2rem");
		expect(style["--appearance-font-size-base"]).toBe("0.9375rem");
		expect(style["--appearance-sidebar-width"]).toBe("18rem");
		expect(style["--appearance-sidebar-width-icon"]).toBe("3.5rem");
		expect(style["--appearance-border-width"]).toBe("2px");
		expect(style["--appearance-shadow-strength"]).toBe("1.5");
	});

	it("selects a readable foreground for tenant colors", () => {
		expect(readableForeground("#ffffff")).toBe("#000000");
		expect(readableForeground("#000000")).toBe("#ffffff");
		expect(readableForeground("#111827")).toBe("#ffffff");
	});
});
