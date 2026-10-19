import { lazy, type ComponentType } from "react";
import type { LucideIcon } from "lucide-react";
import type { Me } from "@/lib/api";
import type { Inbox } from "@/lib/console";

/** A part of Grantra that brings its own pages. A tenant can turn pam, login and itdr off (Settings → Modules). */
export type ModuleId = "platform" | "iga" | "pam" | "login" | "itdr";

/** Where an item sits in the menu, in the order the menu shows them. */
export const groups = [
  { id: "mine", title: "For you" },
  { id: "todo", title: "To do" },
  { id: "privileged", title: "Privileged access" },
  { id: "security", title: "Security" },
  { id: "governance", title: "Governance" },
  { id: "settings", title: "Settings" },
] as const;

export type GroupId = (typeof groups)[number]["id"];

export type Page = ComponentType<{ me: Me }>;

export interface NavItem {
  path: string;
  title: string;
  description: string;
  icon: LucideIcon;
  module: ModuleId;
  /** No group: the item stands on its own, at the top with top set (Home, Inbox), else at the bottom (Help). */
  group?: GroupId;
  top?: boolean;
  /** Whether this person sees the item. The API still checks every call. */
  visible: (me: Me) => boolean;
  page?: Page;
  /** Show the item's title and description above the page; for pages that have no header of their own. */
  header?: boolean;
  /** Pages under the item, shown to whoever sees the item. */
  subpages?: { path: string; page: Page }[];
  /** The milestone issue that builds the page, while it is still a placeholder. */
  issue?: number;
  /** How many things on the page wait on this person, shown next to the item. */
  count?: (inbox: Inbox) => number;
  /**
   * Whether the page changes settings. Governance and Settings pages are
   * read-only for auditors; the API refuses their changes anyway.
   */
  manages?: boolean;
}

/** A page that loads the first time someone opens it. */
export function lazyPage<M>(load: () => Promise<M>, name: keyof M): Page {
  return lazy(() => load().then((m) => ({ default: m[name] as Page })));
}
