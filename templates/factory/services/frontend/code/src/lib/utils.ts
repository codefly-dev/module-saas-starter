import { clsx, type ClassValue } from "clsx";
import { twMerge } from "tailwind-merge";
import { formatDate, formatLimit } from "./admin-core";

export function cn(...inputs: ClassValue[]) {
  return twMerge(clsx(inputs));
}

// Re-export transforms for convenience
export { formatDate, formatLimit };
