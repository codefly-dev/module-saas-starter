// The jobs list is paged by server tokens: each answer carries the token of the
// page after it and nothing else, so going back means remembering the tokens
// already used. The first page's token is the empty string.

export interface PageCursor {
	/** The token of the page on screen. */
	token: string;
	/** The tokens of the pages before it, oldest first. */
	previous: readonly string[];
}

export const FIRST_PAGE: PageCursor = { token: "", previous: [] };

export function nextPage(cursor: PageCursor, nextToken: string): PageCursor {
	if (!nextToken) return cursor;
	return { token: nextToken, previous: [...cursor.previous, cursor.token] };
}

export function previousPage(cursor: PageCursor): PageCursor {
	if (cursor.previous.length === 0) return cursor;
	return {
		token: cursor.previous[cursor.previous.length - 1],
		previous: cursor.previous.slice(0, -1),
	};
}

/** 1 for the first page. */
export function pageNumber(cursor: PageCursor): number {
	return cursor.previous.length + 1;
}
