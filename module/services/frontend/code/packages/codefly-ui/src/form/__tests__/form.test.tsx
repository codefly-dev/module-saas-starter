// @vitest-environment happy-dom
import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";
import {
	Form,
	FormDescription,
	FormField,
	FormLabel,
	FormMessage,
} from "../index.js";

afterEach(cleanup);

describe("Form field primitives", () => {
	it("composes a labelled field with description", () => {
		render(
			<Form>
				<FormField>
					<FormLabel htmlFor="email">Email</FormLabel>
					<input id="email" />
					<FormDescription>We never share it.</FormDescription>
					<FormMessage />
				</FormField>
			</Form>,
		);
		expect(screen.getByText("Email")).toBeTruthy();
		expect(screen.getByText("We never share it.")).toBeTruthy();
	});

	it("FormMessage renders nothing when empty and the error when set", () => {
		const { rerender, container } = render(<FormMessage />);
		expect(container.querySelector("[data-slot=form-message]")).toBeNull();
		rerender(<FormMessage>Required</FormMessage>);
		expect(screen.getByText("Required")).toBeTruthy();
	});
});
