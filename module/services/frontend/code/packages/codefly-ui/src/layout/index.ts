// The shared layout kit: the pure, data-in page primitives every solution's
// Page.tsx and every module UI composes from, so they render one shared package
// instance rather than raw HTML or a re-inlined copy of `@/components/ui`. This
// is the single sealed home for these primitives (issue #451) — the host's
// `src/components/ui/*` re-export from here. Exported from `@codefly-dev/ui/layout`,
// mirroring `@codefly-dev/ui/dashboard`. No host context, no SDK — React only.

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
	Avatar,
	AvatarBadge,
	AvatarFallback,
	AvatarGroup,
	AvatarGroupCount,
	AvatarImage,
} from "./avatar.js";
// Data display
export { Badge, badgeVariants } from "./badge.js";
export { Banner, type BannerProps } from "./banner.js";

// Actions
export { Button, buttonVariants } from "./button.js";
// Page containers
export { Card, type CardProps } from "./card.js";
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
export { Checkbox } from "./checkbox.js";
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
	DateField,
	type DateFieldPart,
	type DateFieldProps,
	type DateFieldVariant,
} from "./date-field.js";
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
// Feedback / state
export { EmptyState, type EmptyStateProps } from "./empty-state.js";
export { ErrorState, type ErrorStateProps } from "./error-state.js";
// The way out of a blocking surface: hiding the close button of `DialogContent`,
// `SheetContent` or `CommandDialog` requires naming what replaces it.
export type { EscapeProps } from "./escape.js";
export { Field, type FieldControlProps, type FieldProps } from "./field.js";
export { Fieldset, FieldsetLegend } from "./fieldset.js";
// Forms
export { Input } from "./input.js";
export {
	InputGroup,
	InputGroupAddon,
	InputGroupButton,
	InputGroupInput,
	InputGroupText,
	InputGroupTextarea,
} from "./input-group.js";
export { Label } from "./label.js";
// A floating, non-modal notice that asks for a decision. Its `escape` is
// required, so it cannot trap the user behind it.
export {
	Notice,
	type NoticeEscape,
	type NoticeProps,
} from "./notice.js";
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
export { Pagination, type PaginationProps } from "./pagination.js";
export {
	PAGE_GAP,
	type PaginationEntry,
	type PaginationRangeOptions,
	paginationRange,
} from "./pagination-model.js";
export {
	SegmentedControl,
	type SegmentedControlOption,
	type SegmentedControlProps,
} from "./segmented-control.js";
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
export { Separator } from "./separator.js";
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
export { Skeleton } from "./skeleton.js";
export { Toaster, toast } from "./sonner.js";
export { Switch } from "./switch.js";
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
export { type TabItem, Tabs, type TabsProps } from "./tabs.js";
export {
	TabsContent,
	TabsList,
	TabsRoot,
	TabsTrigger,
	tabsListVariants,
} from "./tabs-root.js";
export { Textarea } from "./textarea.js";
export {
	Tooltip,
	TooltipContent,
	TooltipProvider,
	TooltipTrigger,
} from "./tooltip.js";
export { useIsMobile } from "./use-mobile.js";
