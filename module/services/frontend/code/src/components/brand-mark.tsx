"use client";

import Image from "next/image";
import { useAppearance } from "@/lib/appearance-provider";
import { cn } from "@/lib/utils";

export function BrandMark({
	className,
	imageClassName,
}: {
	className?: string;
	imageClassName?: string;
}) {
	const { branding } = useAppearance();
	if (!branding.logo) return <span className={className}>{branding.mark}</span>;
	const imageClasses = cn("h-full w-full object-contain", imageClassName);
	return (
		<span className={className}>
			<Image
				src={branding.logo.lightSrc}
				alt={branding.logo.alt}
				width={64}
				height={64}
				unoptimized
				className={cn(imageClasses, branding.logo.darkSrc && "dark:hidden")}
			/>
			{branding.logo.darkSrc ? (
				<Image
					src={branding.logo.darkSrc}
					alt={branding.logo.alt}
					width={64}
					height={64}
					unoptimized
					className={cn(imageClasses, "hidden dark:block")}
				/>
			) : null}
		</span>
	);
}
