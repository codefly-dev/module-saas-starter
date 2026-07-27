import {
	cleanup,
	fireEvent,
	render,
	screen,
	waitFor,
} from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";
import { ThemeProvider, useTheme } from "../theme-provider";

function Consumer() {
	const { theme, resolvedTheme, setTheme } = useTheme();
	return (
		<button type="button" onClick={() => setTheme("light")}>
			{theme}:{resolvedTheme}
		</button>
	);
}

afterEach(() => {
	cleanup();
	window.localStorage.clear();
	document.documentElement.classList.remove("dark");
	document.documentElement.style.colorScheme = "";
});

describe("ThemeProvider", () => {
	it("applies a persisted theme without rendering a script element", async () => {
		window.localStorage.setItem("theme", "dark");
		const rendered = render(
			<ThemeProvider defaultTheme="system" enableSystem>
				<Consumer />
			</ThemeProvider>,
		);

		await waitFor(() =>
			expect(screen.getByRole("button").textContent).toBe("dark:dark"),
		);
		expect(document.documentElement.classList.contains("dark")).toBe(true);
		expect(rendered.container.querySelector("script")).toBeNull();

		fireEvent.click(screen.getByRole("button"));
		expect(document.documentElement.classList.contains("dark")).toBe(false);
		expect(window.localStorage.getItem("theme")).toBe("light");
	});
});
