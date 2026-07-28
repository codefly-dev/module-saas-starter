export async function register() {
  if (process.env.NEXT_RUNTIME === "nodejs") {
    process.stdout.write(
      `${JSON.stringify({
        level: "info",
        message: "service.started",
        service: "marketing",
        release: process.env.MARKETING_RELEASE ?? "development",
      })}\n`,
    );
  }
}
