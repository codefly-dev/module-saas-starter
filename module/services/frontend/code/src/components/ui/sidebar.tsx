"use client";

import { SidebarProvider as KitSidebarProvider } from "@codefly-dev/ui/layout";
import type { ComponentProps } from "react";

export {
	Sidebar,
	SidebarContent,
	SidebarFooter,
	SidebarGroup,
	SidebarGroupAction,
	SidebarGroupContent,
	SidebarGroupLabel,
	SidebarHeader,
	SidebarInput,
	SidebarInset,
	SidebarMenu,
	SidebarMenuAction,
	SidebarMenuBadge,
	SidebarMenuButton,
	SidebarMenuItem,
	SidebarMenuSkeleton,
	SidebarMenuSub,
	SidebarMenuSubButton,
	SidebarMenuSubItem,
	SidebarRail,
	SidebarSeparator,
	SidebarTrigger,
	useSidebar,
} from "@codefly-dev/ui/layout";

export function SidebarProvider({
	onOpenChange,
	...props
}: ComponentProps<typeof KitSidebarProvider>) {
	return (
		<KitSidebarProvider
			{...props}
			onOpenChange={(open) => {
				document.cookie = `sidebar_state=${open}; path=/; max-age=${60 * 60 * 24 * 7}`;
				onOpenChange?.(open);
			}}
		/>
	);
}
