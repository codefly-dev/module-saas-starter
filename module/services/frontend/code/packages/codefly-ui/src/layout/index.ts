// The shared layout kit: the pure, data-in page primitives every solution's
// Page.tsx and every module UI composes from, so they render one shared package
// instance rather than raw HTML or a re-inlined copy of `@/components/ui`. This
// is the single sealed home for these primitives (issue #451) — the host's
// `src/components/ui/*` re-export from here. Exported from `@codefly-dev/ui/layout`,
// mirroring `@codefly-dev/ui/dashboard`. No host context, no SDK — React only.

// Page containers
export { Card, type CardProps, Section, type SectionProps } from "./card.js";
export { type TabItem, Tabs, type TabsProps } from "./tabs.js";

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
	TableFooter,
	TableHead,
	TableHeader,
	TableRow,
} from "./table.js";
export { Skeleton } from "./skeleton.js";
export { Separator } from "./separator.js";

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
