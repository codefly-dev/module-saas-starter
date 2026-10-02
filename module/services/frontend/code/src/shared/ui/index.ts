// Host convenience imports; presentation is implemented by the shared kit.

// Metric charts for the template dashboard — take metric `data`, not raw RPC.
export {
	MetricAreaChart as AreaChart,
	MetricBarChart as BarChart,
	type ChartDatum,
	type ChartSeries,
	chartSeriesColor,
	MetricLineChart as LineChart,
	type MetricChartProps,
} from "@codefly-dev/ui/dashboard";
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
} from "@codefly-dev/ui/layout";
export { Avatar, AvatarFallback, AvatarImage } from "@codefly-dev/ui/layout";
export { Badge, badgeVariants } from "@codefly-dev/ui/layout";
export { Button, buttonVariants } from "@codefly-dev/ui/layout";
export {
	SegmentedControl,
	type SegmentedControlOption,
} from "@codefly-dev/ui/layout";
export {
	CardRoot as Card,
	CardContent,
	CardDescription,
	CardFooter,
	CardHeader,
	CardTitle,
} from "@codefly-dev/ui/layout";
export { Checkbox } from "@codefly-dev/ui/layout";
export {
	Dialog,
	DialogContent,
	DialogDescription,
	DialogFooter,
	DialogHeader,
	DialogTitle,
	DialogTrigger,
} from "@codefly-dev/ui/layout";
export {
	DropdownMenu,
	DropdownMenuContent,
	DropdownMenuGroup,
	DropdownMenuItem,
	DropdownMenuLabel,
	DropdownMenuSeparator,
	DropdownMenuTrigger,
} from "@codefly-dev/ui/layout";
export { Input } from "@codefly-dev/ui/layout";
export { Label } from "@codefly-dev/ui/layout";
export {
	Select,
	SelectContent,
	SelectItem,
	SelectTrigger,
	SelectValue,
} from "@codefly-dev/ui/layout";
export { Separator } from "@codefly-dev/ui/layout";
export { Sheet, SheetContent, SheetTrigger } from "@codefly-dev/ui/layout";
export { Skeleton } from "@codefly-dev/ui/layout";
// The never-flash loading primitive: nothing for the first 200ms of a wait, then
// an indicator that stays long enough to read. Re-exported here because this
// file is the host's one import surface for the kit, and a rule with no import
// path is a rule surfaces cannot follow — before this, `src/` had zero call
// sites and every surface hand-rolled a "Loading…" that flashed. Prefer
// `useLoadingPhase` wherever an empty state sits below the wait.
export {
	DelayedLoading,
	type DelayedLoadingOptions,
	type DelayedLoadingProps,
	LOADING_DELAY_MS,
	LOADING_MIN_VISIBLE_MS,
	type LoadingPhase,
	Spinner,
	type SpinnerProps,
	useDelayedLoading,
	useLoadingPhase,
} from "@codefly-dev/ui/layout";
export { Toaster } from "@/components/ui/sonner";
export { Switch } from "@codefly-dev/ui/layout";
export {
	Table,
	TableBody,
	TableCell,
	TableHead,
	TableHeader,
	TableRow,
} from "@codefly-dev/ui/layout";
export {
	TabsRoot as Tabs,
	TabsContent,
	TabsList,
	TabsTrigger,
} from "@codefly-dev/ui/layout";
export { Textarea } from "@codefly-dev/ui/layout";
export {
	Tooltip,
	TooltipContent,
	TooltipProvider,
	TooltipTrigger,
} from "@codefly-dev/ui/layout";
export {
	Popover,
	PopoverContent,
	PopoverDescription,
	PopoverTitle,
	PopoverTrigger,
} from "@codefly-dev/ui/layout";
export {
	Grid,
	Layout,
	Page,
	PageHeader,
	Panel,
	Section,
	Stack,
} from "@codefly-dev/ui/layout";

// Dashboard value-display widgets — bound to a metric's presentational data.
export {
	formatMetricValue,
	KPIRow,
	type Metric,
	MetricCard,
	type MetricFormat,
	StatTile,
} from "@codefly-dev/ui/dashboard";

export { Banner } from "@codefly-dev/ui/layout";
export { Notice } from "@codefly-dev/ui/layout";
