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
