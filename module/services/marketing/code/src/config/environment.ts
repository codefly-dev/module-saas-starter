import { z } from "zod";

const localOrHTTPSURL = z.string().url().superRefine((value, context) => {
  const url = new URL(value);
  if (
    url.username ||
    url.password ||
    (url.protocol !== "https:" &&
      url.hostname !== "localhost" &&
      !url.hostname.endsWith(".localhost") &&
      !url.hostname.endsWith(".svc.cluster.local"))
  ) {
    context.addIssue({
      code: z.ZodIssueCode.custom,
      message: "must be HTTPS outside localhost or an in-cluster service",
    });
  }
});

const environmentSchema = z
  .object({
    MARKETING_ENABLED: z.enum(["true", "false"]).default("true"),
    MARKETING_INDEXABLE: z.enum(["true", "false"]).default("false"),
    MARKETING_CATALOG_URL: localOrHTTPSURL.optional(),
    MARKETING_RELEASE: z.string().trim().min(1).max(120).default("development"),
  })
  .strict();

export type MarketingEnvironment = {
  enabled: boolean;
  indexable: boolean;
  catalogURL?: string;
  release: string;
};

let cached: MarketingEnvironment | undefined;

export function marketingEnvironment(
  input: Record<string, string | undefined> = process.env,
): MarketingEnvironment {
  if (input === process.env && cached) return cached;
  const parsed = environmentSchema.safeParse({
    MARKETING_ENABLED: input.MARKETING_ENABLED,
    MARKETING_INDEXABLE: input.MARKETING_INDEXABLE,
    MARKETING_CATALOG_URL: input.MARKETING_CATALOG_URL,
    MARKETING_RELEASE: input.MARKETING_RELEASE,
  });
  if (!parsed.success) {
    throw new Error(
      `Invalid public marketing environment:\n${parsed.error.issues
        .map((issue) => `- ${issue.path.join(".")}: ${issue.message}`)
        .join("\n")}`,
    );
  }
  const environment = {
    enabled: parsed.data.MARKETING_ENABLED === "true",
    indexable: parsed.data.MARKETING_INDEXABLE === "true",
    catalogURL: parsed.data.MARKETING_CATALOG_URL,
    release: parsed.data.MARKETING_RELEASE,
  };
  if (input === process.env) cached = environment;
  return environment;
}
