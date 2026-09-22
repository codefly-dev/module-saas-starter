// The shared layout kit: the pure, data-in page primitives every solution's
// Page.tsx and every module UI composes from, so they render one shared package
// instance rather than raw HTML or a re-inlined copy of `@/components/ui`. This
// is the single sealed home for these primitives (issue #451) — the host's
// `src/components/ui/*` re-export from here. Exported from `@codefly-dev/ui/layout`,
// mirroring `@codefly-dev/ui/dashboard`. No host context, no SDK — React only.

// Page containers
export { Card, type CardProps } from "./card.js";
export { type TabItem, Tabs, type TabsProps } from "./tabs.js";

// Feedback / state
export { EmptyState, type EmptyStateProps } from "./empty-state.js";
export { ErrorState, type ErrorStateProps } from "./error-state.js";

// Actions
export { Button, buttonVariants } from "./button.js";

// Forms
export { Input } from "./input.js";
export { Textarea } from "./textarea.js";
export { Label } from "./label.js";
export { Checkbox } from "./checkbox.js";
export { Switch } from "./switch.js";
export {
	Select,
	SelectContent,
	SelectGroup,
	SelectItem,
	SelectLabel,
	SelectScrollDownButton,
	SelectScrollUpButton,
	SelectSeparator,
	SelectTrigger,
	SelectValue,
} from "./select.js";
export { Field, type FieldControlProps, type FieldProps } from "./field.js";
export { Fieldset, FieldsetLegend } from "./fieldset.js";
export {
	DateField,
	type DateFieldPart,
	type DateFieldProps,
	type DateFieldVariant,
} from "./date-field.js";
export {
	SegmentedControl,
	type SegmentedControlOption,
	type SegmentedControlProps,
} from "./segmented-control.js";

// Data display
export { Badge, badgeVariants } from "./badge.js";
export {
	Avatar,
	AvatarBadge,
	AvatarFallback,
	AvatarGroup,
	AvatarGroupCount,
	AvatarImage,
} from "./avatar.js";
export {
	Table,
	TableBody,
	TableCaption,
	TableCell,
	TableEmptyState,
	TableFooter,
	TableHead,
	TableHeader,
	TableRow,
	TableToolbar,
} from "./table.js";
export { Skeleton } from "./skeleton.js";
export { Separator } from "./separator.js";
export { Pagination, type PaginationProps } from "./pagination.js";
export {
	PAGE_GAP,
	type PaginationEntry,
	type PaginationRangeOptions,
	paginationRange,
} from "./pagination-model.js";

// Overlays
export {
	Dialog,
	DialogClose,
	DialogContent,
	DialogDescription,
	DialogFooter,
	DialogHeader,
	DialogOverlay,
	DialogPortal,
	DialogTitle,
	DialogTrigger,
} from "./dialog.js";
export {
	AlertDialog,
	AlertDialogAction,
	AlertDialogCancel,
	AlertDialogContent,
	AlertDialogDescription,
	AlertDialogFooter,
	AlertDialogHeader,
	AlertDialogMedia,
	AlertDialogOverlay,
	AlertDialogPortal,
	AlertDialogTitle,
	AlertDialogTrigger,
} from "./alert-dialog.js";
export {
	Tooltip,
	TooltipContent,
	TooltipProvider,
	TooltipTrigger,
} from "./tooltip.js";
export {
	DropdownMenu,
	DropdownMenuCheckboxItem,
	DropdownMenuContent,
	DropdownMenuGroup,
	DropdownMenuItem,
	DropdownMenuLabel,
	DropdownMenuPortal,
	DropdownMenuRadioGroup,
	DropdownMenuRadioItem,
	DropdownMenuSeparator,
	DropdownMenuShortcut,
	DropdownMenuSub,
	DropdownMenuSubContent,
	DropdownMenuSubTrigger,
	DropdownMenuTrigger,
} from "./dropdown-menu.js";

export {
	CardAction,
	CardContent,
	CardDescription,
	CardEyebrow,
	CardFooter,
	CardHeader,
	CardMetadata,
	CardRoot,
	CardTitle,
} from "./card-root.js";

export {
	TabsContent,
	TabsList,
	TabsRoot,
	TabsTrigger,
	tabsListVariants,
} from "./tabs-root.js";

export {
	Command,
	CommandDialog,
	CommandEmpty,
	CommandGroup,
	CommandInput,
	CommandItem,
	CommandList,
	CommandSeparator,
	CommandShortcut,
	useCommandFilter,
} from "./command.js";

export {
	InputGroup,
	InputGroupAddon,
	InputGroupButton,
	InputGroupInput,
	InputGroupText,
	InputGroupTextarea,
} from "./input-group.js";

export {
	Sheet,
	SheetClose,
	SheetContent,
	SheetDescription,
	SheetFooter,
	SheetHeader,
	SheetTitle,
	SheetTrigger,
} from "./sheet.js";

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
	SidebarProvider,
	SidebarRail,
	SidebarSeparator,
	SidebarTrigger,
	useSidebar,
} from "./sidebar.js";

export { Toaster } from "./sonner.js";

export { useIsMobile } from "./use-mobile.js";

export type { SectionProps } from "./page.js";
export {
	Grid,
	Layout,
	Page,
	PageHeader,
	Panel,
	Section,
	Stack,
} from "./page.js";

export { Banner, type BannerProps } from "./banner.js";
