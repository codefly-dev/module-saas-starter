"use client";

import {
	PluginTransportConfigurationError,
	usePluginRuntime,
} from "@codefly-dev/ui/plugin-host/runtime";
import { useState } from "react";
import {
	Badge,
	Button,
	Card,
	CardContent,
	CardDescription,
	CardHeader,
	CardTitle,
	Input,
	Label,
	Table,
	TableBody,
	TableCell,
	TableHead,
	TableHeader,
	TableRow,
} from "@/shared/ui";
import {
	TRANSPORT_DEMO_ROUTES,
	type TransportDemoRoute,
	transportCarries,
	transportDemoPlugin,
	transportMutates,
} from "../model/demo-plugin";

const BODY_PREVIEW = 400;

function routeKey(route: TransportDemoRoute): string {
	return `${route.alias}/${route.path}`;
}

/** Whether this demo alias is the Connect kind, read from the declaration. */
function connectAlias(alias: string): boolean {
	return (
		transportDemoPlugin.services?.find((service) => service.alias === alias)
			?.protocol === "connect"
	);
}

type Answer =
	| { kind: "refused-here"; detail: string }
	| { kind: "answered"; status: number; requestId: string; body: string }
	| { kind: "unreachable"; detail: string };

/**
 * The plugin transport playground: send each shape a module's HTTP bindings
 * produce through the host's real transport, and read what the host answers.
 *
 * It exists for one property. The transport is a same-origin fetch over the
 * host's BFF, and until now every segment had to match `[A-Za-z0-9._~-]+`, so a
 * binding written in Google's custom-verb form — `records:recover`,
 * `commands:recover`, `interactions/{id}:respond` — was refused in the browser
 * before any request left it. A contributed page therefore reached LESS than
 * the module's own bindings served, and a composition had to take a second
 * transport from somewhere else to recover a lost admission or answer an
 * interaction. The custom-verb rows below are that fix, driven.
 *
 * What a press shows depends on what the composition installed, and the three
 * answers are worth telling apart:
 *
 *   - "refused here" — the transport would not carry the shape at all; nothing
 *     left the browser. This is where every custom verb used to land.
 *   - a host problem — the shape was carried and the host answered: 404
 *     `plugin_service_not_found` for an alias nobody installed, which is the
 *     canonical host's answer because it composes no plugin service of its own.
 *   - a forwarded answer — the upstream's own status and body, in a composition
 *     that installed this alias.
 *
 * So in this repository the page proves the path gate, not the proxy; the
 * forwarded answer is proved by the transport's own tests, which join this
 * client to the real BFF handler over a stub upstream. That is the honest state
 * of a host that installs no plugin backend rather than a gap in the page.
 *
 * The plugin name is editable so an operator can point the table at a plugin
 * their own composition installed — which is also what makes a press dangerous,
 * because the custom-verb rows are writes. Anything but a read is therefore
 * armed by the first press, naming the plugin and alias it will write to, and
 * sent only by the second; editing the plugin name retracts it.
 */
export function PluginTransportPlayground() {
	const runtime = usePluginRuntime();
	const [plugin, setPlugin] = useState<string>(transportDemoPlugin.name);
	const [answers, setAnswers] = useState<Record<string, Answer>>({});
	// The route a press has armed but not yet sent. A mutating send is never
	// one press: the plugin name above is editable, so the same button that
	// demonstrates a shape against the uninstalled demo would, pointed at a
	// plugin the composition really installed, write to that backend under the
	// operator's own credential. Arming names exactly what will be sent and to
	// whom, and only the second press sends it.
	const [armed, setArmed] = useState<string | null>(null);

	async function send(route: TransportDemoRoute) {
		const key = routeKey(route);
		const connect = connectAlias(route.alias);
		try {
			const service = runtime.service(plugin, route.alias);
			const response = await service.request(route.path, {
				method: route.method,
				headers:
					route.method === "POST"
						? {
								accept: "application/json",
								"content-type": "application/json",
								...(connect ? { "connect-protocol-version": "1" } : {}),
							}
						: { accept: "application/json" },
				body: route.method === "POST" ? "{}" : undefined,
			});
			const body = await response.text();
			setAnswers((current) => ({
				...current,
				[key]: {
					kind: "answered",
					status: response.status,
					requestId: response.headers.get("x-request-id") ?? "",
					body: body.slice(0, BODY_PREVIEW),
				},
			}));
		} catch (error) {
			const detail = error instanceof Error ? error.message : String(error);
			// A configuration error is the transport refusing the shape; anything
			// else is the call failing to complete, which is a different fact.
			setAnswers((current) => ({
				...current,
				[key]: {
					kind:
						error instanceof PluginTransportConfigurationError
							? "refused-here"
							: "unreachable",
					detail,
				},
			}));
		}
	}

	function press(route: TransportDemoRoute) {
		const key = routeKey(route);
		if (transportMutates(route) && armed !== key) {
			setArmed(key);
			return;
		}
		setArmed(null);
		void send(route);
	}

	return (
		<div className="space-y-6">
			<div>
				<h1 data-slot="page-title" className="type-page-title">
					Plugin transport
				</h1>
				<p className="text-muted-foreground">
					A contributed page reaches a module&apos;s backend through this
					host&apos;s transport, under the viewer&apos;s own credential. Send
					each shape a module&apos;s bindings produce and read the answer.
				</p>
			</div>

			<Card>
				<CardHeader>
					<CardTitle>What is installed</CardTitle>
					<CardDescription>
						The transport reaches an installed plugin alias and nothing else.
						The demo plugin below is a declaration this host does not compose,
						so its aliases answer &ldquo;not installed&rdquo; here; name a
						plugin your composition installed to see its forwarded answers.
					</CardDescription>
				</CardHeader>
				<CardContent className="flex flex-wrap items-end gap-4">
					<div className="space-y-2">
						<Label htmlFor="plugin-transport-plugin">Plugin</Label>
						<Input
							id="plugin-transport-plugin"
							className="w-72"
							value={plugin}
							onChange={(event) => {
								// A confirmation named a plugin. Renaming the target
								// retracts it rather than carrying the armed press
								// over to a different backend.
								setArmed(null);
								setPlugin(event.target.value);
							}}
						/>
					</div>
					<dl className="text-sm text-muted-foreground">
						<dt className="font-medium text-foreground">Declared aliases</dt>
						{(transportDemoPlugin.services ?? []).map((service) => (
							<dd key={service.alias}>
								<code>{service.alias}</code> — {service.protocol} at{" "}
								<code>{service.routePrefix}</code>
							</dd>
						))}
					</dl>
				</CardContent>
			</Card>

			<Card>
				<CardHeader>
					<CardTitle>Routes</CardTitle>
					<CardDescription>
						A custom verb (<code>:recover</code>, <code>:respond</code>,{" "}
						<code>:fire</code>) is carried on the last segment, with its colon
						literal. A colon anywhere else, a traversal, a non-procedure path on
						a Connect alias, or a procedure of a service the requirement never
						declared is refused in the browser. Anything but a read asks for
						confirmation first: the plugin above is editable, so a press could
						write to a backend your composition really installed.
					</CardDescription>
				</CardHeader>
				<CardContent>
					<Table>
						<TableHeader>
							<TableRow>
								<TableHead>Shape</TableHead>
								<TableHead>Route</TableHead>
								<TableHead>Carried</TableHead>
								<TableHead>Answer</TableHead>
								<TableHead />
							</TableRow>
						</TableHeader>
						<TableBody>
							{TRANSPORT_DEMO_ROUTES.map((route) => {
								const key = routeKey(route);
								const answer = answers[key];
								const confirming = armed === key;
								return (
									<TableRow key={key}>
										<TableCell>{route.shape}</TableCell>
										<TableCell className="font-mono text-xs">
											{route.method} {route.alias}/{route.path}
										</TableCell>
										<TableCell>
											<Badge
												tone={transportCarries(route) ? "success" : "danger"}
											>
												{transportCarries(route) ? "carried" : "refused"}
											</Badge>
										</TableCell>
										<TableCell className="max-w-96 text-xs">
											<AnswerCell answer={answer} />
										</TableCell>
										<TableCell>
											<Button
												size="sm"
												variant={confirming ? "default" : "outline"}
												onClick={() => {
													press(route);
												}}
											>
												{confirming
													? `Confirm ${route.method} to ${plugin}`
													: "Send"}
											</Button>
											{confirming && (
												<p
													role="status"
													className="text-xs text-muted-foreground"
												>
													This writes to{" "}
													<code>
														{plugin}/{route.alias}
													</code>{" "}
													if your composition installed it. Press again to send.
												</p>
											)}
										</TableCell>
									</TableRow>
								);
							})}
						</TableBody>
					</Table>
				</CardContent>
			</Card>
		</div>
	);
}

function AnswerCell({ answer }: { answer: Answer | undefined }) {
	if (!answer) {
		return <span className="text-muted-foreground">—</span>;
	}
	if (answer.kind === "refused-here") {
		return (
			<div role="status">
				<Badge tone="danger">refused in the browser</Badge>
				<p className="text-muted-foreground">{answer.detail}</p>
			</div>
		);
	}
	if (answer.kind === "unreachable") {
		return (
			<p role="status" className="text-muted-foreground">
				{answer.detail}
			</p>
		);
	}
	return (
		<div role="status" className="space-y-1">
			<Badge tone={answer.status < 400 ? "success" : "warning"}>
				{answer.status}
			</Badge>
			{answer.requestId && (
				<span className="ml-2 text-muted-foreground">
					request {answer.requestId}
				</span>
			)}
			<pre className="whitespace-pre-wrap break-all font-mono">
				{answer.body}
			</pre>
		</div>
	);
}
