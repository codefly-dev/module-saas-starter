"use client";

import {
	CircleCheckIcon,
	InfoIcon,
	Loader2Icon,
	OctagonXIcon,
	TriangleAlertIcon,
} from "lucide-react";
import { Toaster as Sonner, type ToasterProps, toast } from "sonner";

const Toaster = ({ ...props }: ToasterProps) => {
	return (
		<Sonner
			theme="system"
			className="toaster group"
			icons={{
				success: <CircleCheckIcon className="size-4" />,
				info: <InfoIcon className="size-4" />,
				warning: <TriangleAlertIcon className="size-4" />,
				error: <OctagonXIcon className="size-4" />,
				loading: <Loader2Icon className="size-4 animate-spin" />,
			}}
			style={
				{
					"--normal-bg": "var(--popover)",
					"--normal-text": "var(--popover-foreground)",
					"--normal-border": "var(--border)",
					"--border-radius": "var(--radius)",
				} as React.CSSProperties
			}
			toastOptions={{
				classNames: {
					toast: "cn-toast",
				},
			}}
			{...props}
		/>
	);
};

/**
 * Raise a notification in the host's `<Toaster>`. It is the same notification
 * store the Toaster reads, and it reaches a runtime-loaded solution remote
 * through this kit's shared singleton — so a remote calls `toast.error(…)` from
 * `@codefly-dev/ui/layout` and the host shows it. A remote that bundled its own
 * notification library would write to a store no mounted Toaster reads: the
 * message would be lost without a trace.
 */
export { Toaster, toast };
