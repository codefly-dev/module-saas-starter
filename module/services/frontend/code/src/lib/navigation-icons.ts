import {
	Activity,
	ArrowLeftRight,
	Bell,
	BookOpen,
	Bot,
	Building2,
	ChartColumn,
	CreditCard,
	FileText,
	Flag,
	GitCompareArrows,
	Globe,
	HardDriveDownload,
	Key,
	KeyRound,
	Layers,
	LayoutDashboard,
	ListChecks,
	type LucideIcon,
	Mail,
	Monitor,
	Settings,
	Shield,
	ShieldAlert,
	ShieldCheck,
	UserSearch,
	Users,
	UsersRound,
	Wallet,
} from "lucide-react";

import type { FrontendNavigationIcon } from "@/gen/saas/frontend/v1/plugin_catalog";

export const NAVIGATION_ICONS = {
	Activity,
	ArrowLeftRight,
	Bell,
	Bot,
	BookOpen,
	Building2,
	CreditCard,
	ChartColumn,
	FileText,
	Flag,
	Globe,
	GitCompareArrows,
	HardDriveDownload,
	Key,
	KeyRound,
	Layers,
	LayoutDashboard,
	ListChecks,
	Mail,
	Monitor,
	Settings,
	Shield,
	ShieldAlert,
	ShieldCheck,
	UserSearch,
	Users,
	UsersRound,
	Wallet,
} satisfies Record<FrontendNavigationIcon, LucideIcon> &
	Record<string, LucideIcon>;

export function getNavigationIcon(name: string | undefined): LucideIcon {
	return (
		(name && NAVIGATION_ICONS[name as keyof typeof NAVIGATION_ICONS]) ||
		LayoutDashboard
	);
}
