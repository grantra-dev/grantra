import type { Me } from "@/lib/api";
import { iga } from "./iga";
import { itdr } from "./itdr";
import { login } from "./login";
import { pam } from "./pam";
import { helpItem, home, inboxItem, platform } from "./platform";
import { groups, type GroupId, type NavItem } from "./types";

export { groups, type GroupId, type NavItem } from "./types";

/**
 * Every page of the console, from every module. A new feature adds its items
 * to its module's list; the menu, routes and home page follow from here.
 */
export const navItems: NavItem[] = [
  home,
  inboxItem,
  ...iga,
  ...pam,
  ...itdr,
  ...login,
  ...platform,
  helpItem,
];

/** The items this person sees: what their roles show, of the modules the tenant keeps on. */
export const visibleItems = (me: Me) =>
  navItems.filter((i) => !me.disabled_modules?.includes(i.module) && i.visible(me));

export interface NavGroup {
  id: GroupId;
  title: string;
  items: NavItem[];
}

/** The menu groups this person has something in, in menu order. */
export function visibleGroups(me: Me): NavGroup[] {
  const items = visibleItems(me);
  return groups
    .map((g) => ({ id: g.id, title: g.title, items: items.filter((i) => i.group === g.id) }))
    .filter((g) => g.items.length > 0);
}

export const groupTitle = (id: GroupId | undefined) => groups.find((g) => g.id === id)?.title;
