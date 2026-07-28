import { ImageResponse } from "next/og";
import { siteConfig } from "@/config/site";

export const size = { width: 64, height: 64 };
export const contentType = "image/png";

export default function Icon() {
  return new ImageResponse(
    <div
      style={{
        alignItems: "center",
        background: siteConfig.brand.colors.primary,
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
      {siteConfig.brand.mark}
    </div>,
    size,
  );
}
