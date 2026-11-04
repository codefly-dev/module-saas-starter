"use client";

import { Settings } from "lucide-react";
import type { ReactNode } from "react";
import {
	Button,
	Dialog,
	DialogContent,
	DialogDescription,
	DialogTitle,
	DialogTrigger,
	Tabs,
	TabsContent,
	TabsList,
	TabsTrigger,
} from "@/shared/ui";

/** Host navigation stays secondary to the mounted solution's own workspace. */
export function SolutionWorkspace({
	title,
	settings,
	dashboard,
	children,
}: {
	title: string;
	settings?: ReactNode;
	dashboard?: ReactNode;
	children: ReactNode;
}) {
	return (
		<div className="flex min-w-0 flex-col gap-2 px-4 py-2">
			<div className="flex min-h-8 items-center justify-between gap-2">
				<h1
					data-slot="page-title"
					className="text-sm font-normal text-muted-foreground"
				>
					{title}
				</h1>
				{settings}
			</div>
			{dashboard ? (
				<Tabs defaultValue="work" className="w-full">
					<TabsList>
						<TabsTrigger value="work">Workspace</TabsTrigger>
						<TabsTrigger value="dashboard">Dashboard</TabsTrigger>
					</TabsList>
					<TabsContent value="work" keepMounted>
						{children}
					</TabsContent>
					<TabsContent value="dashboard">{dashboard}</TabsContent>
				</Tabs>
			) : (
				children
			)}
		</div>
	);
}

export function SolutionSettings({
	title,
	children,
}: {
	title: string;
	children: ReactNode;
}) {
	return (
		<Dialog>
			<DialogTrigger
				render={
					<Button
						variant="ghost"
						size="icon-sm"
						aria-label={`Settings for ${title}`}
						title="Solution settings"
					/>
				}
			>
				<Settings aria-hidden="true" />
			</DialogTrigger>
			<DialogContent className="max-h-[85dvh] overflow-y-auto sm:max-w-3xl">
				<DialogTitle>{title} settings</DialogTitle>
				<DialogDescription>
					Installation and access for this workspace.
				</DialogDescription>
				{children}
			</DialogContent>
		</Dialog>
	);
}
