"use client";

import { Toaster as KitToaster } from "@codefly-dev/ui/layout";
import type { ComponentProps } from "react";
import { useTheme } from "@/lib/theme-provider";

export function Toaster(props: ComponentProps<typeof KitToaster>) {
	const { theme } = useTheme();
	return <KitToaster theme={theme} {...props} />;
}
