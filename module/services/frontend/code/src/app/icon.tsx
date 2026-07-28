import { ImageResponse } from "next/og";
import { publicSiteConfig } from "@/generated/public-site-config";

export const size = { width: 64, height: 64 };
export const contentType = "image/png";

export default function Icon() {
	return new ImageResponse(
		<div
			style={{
				alignItems: "center",
				background: publicSiteConfig.brand.colors.primary,
				color: "#ffffff",
				display: "flex",
				fontFamily: "Arial",
				fontSize: 34,
				fontWeight: 800,
				height: "100%",
				justifyContent: "center",
				width: "100%",
			}}
		>
			{publicSiteConfig.brand.mark}
		</div>,
		size,
	);
}
