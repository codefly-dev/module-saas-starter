import type { ReactElement } from "react";

/**
 * The way out of a blocking surface — `DialogContent`, `SheetContent`,
 * `CommandDialog`.
 *
 * The kit's close button is on by default. A caller may take it away, but only
 * by naming what replaces it: `showCloseButton={false}` without an `escape` does
 * not compile, and neither does a computed `showCloseButton={someBoolean}`,
 * because `boolean` includes `false`. A surface the user cannot leave is
 * therefore not something a caller can write by omission.
 *
 * `escape` is a `ReactElement`, not a `ReactNode`, so `null`, `false`, a string
 * or a conditional that can come out empty cannot stand in for it. The kit
 * renders it inside the popup (after the caller's children), so it is always in
 * the surface's own tab order. It is typically a `DialogClose`/`SheetClose`, a
 * footer whose button closes the surface, or an action that ends the session.
 *
 * What the type cannot see: an `escape` the caller renders `disabled`
 * indefinitely, or an `onOpenChange` that refuses every close. Those remain the
 * caller's responsibility — and a review's.
 */
export type EscapeProps =
	| {
			/** Render the kit's close button (the default). */
			showCloseButton?: true;
			escape?: never;
	  }
	| {
			showCloseButton: false;
			/** What replaces the close button. Required, and always rendered. */
			escape: ReactElement;
	  };
