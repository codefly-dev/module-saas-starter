import type { ReactNode } from "react";

// Layout is the page shell a dashboard drops into: an optional title/description
// header over a vertical stack of sections. It owns page rhythm so a consumer
// writes <Layout><Dashboard data={…}/></Layout> and nothing else.
export function Layout({
	title,
	description,
	children,
}: {
	title?: string;
	description?: string;
	children: ReactNode;
}) {
	return (
		<div className="space-y-6">
			{(title || description) && (
				<header className="space-y-1">
					{title && (
						<h1 data-slot="page-title" className="type-page-title">{title}</h1>
					)}
					{description && (
						<p className="text-muted-foreground">{description}</p>
					)}
				</header>
			)}
			{children}
		</div>
	);
}
