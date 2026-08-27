// Re-export shadcn/ui components from their canonical location.
// Feature slices import from "@/shared/ui" rather than "@/components/ui".

export {
	AlertDialog,
	AlertDialogAction,
	AlertDialogCancel,
	AlertDialogContent,
	AlertDialogDescription,
	AlertDialogFooter,
	AlertDialogHeader,
	AlertDialogTitle,
	AlertDialogTrigger,
} from "@/components/ui/alert-dialog";
export { Avatar, AvatarFallback, AvatarImage } from "@/components/ui/avatar";
export { Badge, badgeVariants } from "@/components/ui/badge";
export { Button, buttonVariants } from "@/components/ui/button";
export {
	Card,
	CardContent,
	CardDescription,
	CardFooter,
	CardHeader,
	CardTitle,
} from "@/components/ui/card";
export { Checkbox } from "@/components/ui/checkbox";
export {
	Dialog,
	DialogContent,
	DialogDescription,
	DialogFooter,
	DialogHeader,
	DialogTitle,
	DialogTrigger,
} from "@/components/ui/dialog";
export {
	DropdownMenu,
	DropdownMenuContent,
	DropdownMenuGroup,
	DropdownMenuItem,
	DropdownMenuLabel,
	DropdownMenuSeparator,
	DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
export { Input } from "@/components/ui/input";
export { Label } from "@/components/ui/label";
export {
	Select,
	SelectContent,
	SelectItem,
	SelectTrigger,
	SelectValue,
} from "@/components/ui/select";
export { Separator } from "@/components/ui/separator";
export { Sheet, SheetContent, SheetTrigger } from "@/components/ui/sheet";
export { Skeleton } from "@/components/ui/skeleton";
export { Toaster } from "@/components/ui/sonner";
export { Switch } from "@/components/ui/switch";
export {
	Table,
	TableBody,
	TableCell,
	TableHead,
	TableHeader,
	TableRow,
} from "@/components/ui/table";
export { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
export { Textarea } from "@/components/ui/textarea";
export {
	Tooltip,
	TooltipContent,
	TooltipProvider,
	TooltipTrigger,
} from "@/components/ui/tooltip";

// Dashboard value-display widgets — bound to a metric's presentational data.
export {
	formatMetricValue,
	KPIRow,
	type Metric,
	MetricCard,
	type MetricFormat,
	StatTile,
} from "./metric-tiles";
