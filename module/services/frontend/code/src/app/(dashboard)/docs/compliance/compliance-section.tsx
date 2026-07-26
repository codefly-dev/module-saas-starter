"use client";

import {
	AlertTriangle,
	ChevronDown,
	FileText,
	Lock,
	Server,
	ShieldCheck,
	Users,
} from "lucide-react";
import { useState } from "react";
import {
	Card,
	CardContent,
	CardDescription,
	CardHeader,
	CardTitle,
} from "@/shared/ui";

const ICONS = {
	security: ShieldCheck,
	lock: Lock,
	users: Users,
	server: Server,
	warning: AlertTriangle,
	document: FileText,
} as const;

export function ComplianceSection({
	icon,
	title,
	description,
	children,
	defaultOpen = false,
}: {
	icon: keyof typeof ICONS;
	title: string;
	description: string;
	children: React.ReactNode;
	defaultOpen?: boolean;
}) {
	const [open, setOpen] = useState(defaultOpen);
	const Icon = ICONS[icon];

	return (
		<Card>
			<button
				type="button"
				onClick={() => setOpen(!open)}
				className="w-full text-left"
			>
				<CardHeader className="flex flex-row items-center gap-4 space-y-0">
					<div className="flex h-10 w-10 shrink-0 items-center justify-center rounded-lg bg-primary/10">
						<Icon className="h-5 w-5 text-primary" />
					</div>
					<div className="flex-1 min-w-0">
						<CardTitle className="text-base">{title}</CardTitle>
						<CardDescription className="mt-1">{description}</CardDescription>
					</div>
					<ChevronDown
						className={`h-5 w-5 shrink-0 text-muted-foreground transition-transform ${
							open ? "rotate-180" : ""
						}`}
					/>
				</CardHeader>
			</button>
			{open && (
				<CardContent className="pt-0 pl-[4.5rem]">
					<div className="prose prose-sm dark:prose-invert max-w-none">
						{children}
					</div>
				</CardContent>
			)}
		</Card>
	);
}
