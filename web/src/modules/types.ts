import { lazy, type ComponentType } from "react";
import type { LucideIcon } from "lucide-react";
import type { Me } from "@/lib/api";

/** A part of Grantra that brings its own pages. A tenant may later switch some off. */
export type ModuleId = "platform" | "iga" | "pam" | "login";

/** Where an item sits in the menu, in the order the menu shows them. */
export const groups = [
  { id: "mine", title: "For you" },
  { id: "todo", title: "To do" },
  { id: "privileged", title: "Privileged access" },
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
  /** No group: the item stands on its own at the top (Home) or bottom (Help). */
  group?: GroupId;
  /** Whether this person sees the item. The API still checks every call. */
  visible: (me: Me) => boolean;
  page?: Page;
  /** Show the item's title and description above the page; for pages that have no header of their own. */
  header?: boolean;
  /** Pages under the item, shown to whoever sees the item. */
  subpages?: { path: string; page: Page }[];
  /** The milestone issue that builds the page, while it is still a placeholder. */
  issue?: number;
}

/** A page that loads the first time someone opens it. */
export function lazyPage<M>(load: () => Promise<M>, name: keyof M): Page {
  return lazy(() => load().then((m) => ({ default: m[name] as Page })));
}
