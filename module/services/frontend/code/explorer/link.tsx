import type { ComponentProps } from "react";
import { toast } from "sonner";
/** App routes are displayed as preview actions rather than leaving Storybook. */
export default function PreviewLink({
	href,
	onClick,
	...props
}: ComponentProps<"a">) {
	return (
		<a
			{...props}
			href={href}
			onClick={(event) => {
				onClick?.(event);
				event.preventDefault();
				toast.info(`Preview navigation: ${href}`);
			}}
		/>
	);
}
