// @vitest-environment happy-dom
import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";
import { Card, Section } from "../card.js";

afterEach(cleanup);

describe("Card", () => {
	it("renders title, actions, and children", () => {
		render(
			<Card title="Sources" actions={<button type="button">Add</button>}>
				<p>body</p>
			</Card>,
		);
		expect(screen.getByRole("heading", { name: "Sources" })).toBeTruthy();
		expect(screen.getByRole("button", { name: "Add" })).toBeTruthy();
		expect(screen.getByText("body")).toBeTruthy();
	});

	it("omits the header row when neither title nor actions is given", () => {
		render(
			<Card>
				<p>only body</p>
			</Card>,
		);
		expect(screen.queryByRole("heading")).toBeNull();
		expect(screen.getByText("only body")).toBeTruthy();
	});
});

describe("Section", () => {
	it("renders title, description, and children", () => {
		render(
			<Section title="Overview" description="what this shows">
				<p>content</p>
			</Section>,
		);
		expect(screen.getByRole("heading", { name: "Overview" })).toBeTruthy();
		expect(screen.getByText("what this shows")).toBeTruthy();
		expect(screen.getByText("content")).toBeTruthy();
	});

	it("omits the header block when neither title nor description is given", () => {
		render(
			<Section>
				<p>bare</p>
			</Section>,
		);
		expect(screen.queryByRole("heading")).toBeNull();
		expect(screen.getByText("bare")).toBeTruthy();
	});
});
