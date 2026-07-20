import { cn } from "@/lib/utils";

const variants = {
	success:
		"bg-green-100 text-green-800 dark:bg-green-900/30 dark:text-green-400",
	warning:
		"bg-yellow-100 text-yellow-800 dark:bg-yellow-900/30 dark:text-yellow-400",
	error: "bg-red-100 text-red-800 dark:bg-red-900/30 dark:text-red-400",
	default: "bg-muted text-muted-foreground",
};

export function StatusBadge({
	label,
	variant = "default",
}: {
	label: string;
	variant?: keyof typeof variants;
}) {
	return (
		<span
			className={cn(
				"inline-flex items-center px-2 py-0.5 rounded-full text-xs font-medium",
				variants[variant],
			)}
		>
			{label}
		</span>
	);
}
