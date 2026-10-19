import { clsx, type ClassValue } from "clsx";
import { twMerge } from "tailwind-merge";

export function cn(...inputs: ClassValue[]) {
  return twMerge(clsx(inputs));
}

const dateFormat = new Intl.DateTimeFormat(undefined, { dateStyle: "medium" });

export function formatDate(iso: string) {
  return dateFormat.format(new Date(iso));
}

const timeFormat = new Intl.DateTimeFormat(undefined, { dateStyle: "medium", timeStyle: "short" });

export function formatTime(iso: string) {
  return timeFormat.format(new Date(iso));
}

/**
 * Whether every word of query appears in one of texts, whatever the case:
 * how every filter box in the console matches. An empty query matches all.
 */
export function hasWords(query: string, ...texts: (string | undefined)[]): boolean {
  const words = query.toLowerCase().split(/\s+/).filter(Boolean);
  const text = texts.filter(Boolean).join(" ").toLowerCase();
  return words.every((w) => text.includes(w));
}

/** "critical" reads as "Critical". */
export const capitalize = (s: string) => s.charAt(0).toUpperCase() + s.slice(1);

/** An identifier such as "platform_role.granted" or "cost_center" in words: "Platform role granted". */
export const humanize = (id: string) => capitalize(id.replace(/[._]/g, " ").trim());
