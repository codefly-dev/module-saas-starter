/**
 * Chat — the bound primitive over a retrieval connector.
 *
 * The component is a pure function of the {@link RetrievalConnector} interface:
 * it hands the connector a query and renders the {@link GroundedAnswer} it gets
 * back. Every claim carries its citations as clickable refs (click →
 * `document@version#span`), and a refusal renders as an explicit "no source"
 * state. Because it depends only on the interface, swapping the stub connector
 * for a real model or retriever never moves this UI.
 */

"use client";

import { type FormEvent, useCallback, useId, useState } from "react";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Textarea } from "@/components/ui/textarea";
import { formatRef } from "../model/connector";
import type { GroundedAnswer, Ref, RetrievalConnector } from "../model/types";

interface AssistantTurn {
	role: "assistant";
	answer: GroundedAnswer;
}

interface UserTurn {
	role: "user";
	text: string;
}

interface PendingTurn {
	role: "assistant";
	answer: null;
}

type Turn = UserTurn | AssistantTurn | PendingTurn;

export interface ChatProps {
	connector: RetrievalConnector;
	/** Invoked when a citation ref is clicked, e.g. to open the source. */
	onOpenRef?: (ref: Ref) => void;
	placeholder?: string;
}

function CitationRef({
	refValue,
	onOpenRef,
}: {
	refValue: Ref;
	onOpenRef?: (ref: Ref) => void;
}) {
	const address = formatRef(refValue);
	return (
		<Button
			type="button"
			variant="link"
			size="xs"
			data-slot="chat-citation"
			data-ref={address}
			title={address}
			onClick={() => onOpenRef?.(refValue)}
		>
			{address}
		</Button>
	);
}

function AnsweredTurn({
	answer,
	onOpenRef,
}: {
	answer: Extract<GroundedAnswer, { status: "answered" }>;
	onOpenRef?: (ref: Ref) => void;
}) {
	return (
		<div data-slot="chat-answer" className="space-y-3">
			<div className="space-y-2">
				{answer.claims.map((claim, i) => (
					<p
						key={`${i}-${claim.text}`}
						data-slot="chat-claim"
						className="text-sm"
					>
						{claim.text}{" "}
						<span className="inline-flex flex-wrap gap-1 align-middle">
							{claim.refs.map((ref) => (
								<CitationRef
									key={formatRef(ref)}
									refValue={ref}
									onOpenRef={onOpenRef}
								/>
							))}
						</span>
					</p>
				))}
			</div>
			<ul
				data-slot="chat-citations"
				className="space-y-1 border-t pt-2 text-xs text-muted-foreground"
			>
				{answer.citations.map((citation) => (
					<li key={formatRef(citation.ref)} className="flex gap-2">
						<span className="font-mono">{formatRef(citation.ref)}</span>
						<span className="truncate">{citation.snippet}</span>
					</li>
				))}
			</ul>
		</div>
	);
}

function AssistantMessage({
	answer,
	onOpenRef,
}: {
	answer: GroundedAnswer;
	onOpenRef?: (ref: Ref) => void;
}) {
	if (answer.status === "refused") {
		return (
			<p data-slot="chat-refusal" className="text-sm text-muted-foreground">
				{answer.reason}
			</p>
		);
	}
	return <AnsweredTurn answer={answer} onOpenRef={onOpenRef} />;
}

function TurnMessage({
	turn,
	onOpenRef,
}: {
	turn: Turn;
	onOpenRef?: (ref: Ref) => void;
}) {
	if (turn.role === "user") {
		return (
			<div
				data-slot="chat-turn"
				data-role="user"
				className="text-sm font-medium"
			>
				{turn.text}
			</div>
		);
	}
	return (
		<div data-slot="chat-turn" data-role="assistant">
			{turn.answer === null ? (
				<p data-slot="chat-pending" className="text-sm text-muted-foreground">
					Searching sources…
				</p>
			) : (
				<AssistantMessage answer={turn.answer} onOpenRef={onOpenRef} />
			)}
		</div>
	);
}

export function Chat({ connector, onOpenRef, placeholder }: ChatProps) {
	const [turns, setTurns] = useState<Turn[]>([]);
	const [input, setInput] = useState("");
	const [pending, setPending] = useState(false);
	const inputId = useId();

	const submit = useCallback(
		async (event: FormEvent) => {
			event.preventDefault();
			const text = input.trim();
			if (!text || pending) return;
			setInput("");
			setPending(true);
			setTurns((prev) => [
				...prev,
				{ role: "user", text },
				{ role: "assistant", answer: null },
			]);
			const answer = await connector.answer({ text });
			setTurns((prev) => {
				const next = [...prev];
				next[next.length - 1] = { role: "assistant", answer };
				return next;
			});
			setPending(false);
		},
		[connector, input, pending],
	);

	return (
		<Card data-slot="chat" className="flex h-full flex-col">
			<CardHeader>
				<CardTitle className="text-base">Ask the knowledge base</CardTitle>
			</CardHeader>
			<CardContent className="flex flex-1 flex-col gap-4">
				<div data-slot="chat-transcript" className="flex-1 space-y-4">
					{turns.map((turn, i) => (
						<TurnMessage
							key={`${i}-${turn.role}`}
							turn={turn}
							onOpenRef={onOpenRef}
						/>
					))}
				</div>
				<form onSubmit={submit} className="space-y-2">
					<label htmlFor={inputId} className="sr-only">
						Question
					</label>
					<Textarea
						id={inputId}
						value={input}
						onChange={(event) => setInput(event.target.value)}
						placeholder={placeholder ?? "Ask a question about your sources…"}
						rows={2}
					/>
					<div className="flex justify-end">
						<Button
							type="submit"
							disabled={pending || input.trim().length === 0}
						>
							Ask
						</Button>
					</div>
				</form>
			</CardContent>
		</Card>
	);
}
