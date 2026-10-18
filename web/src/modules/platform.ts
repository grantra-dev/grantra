import { CircleHelp, FileSearch, House, KeyRound, Users } from "lucide-react";
import { PlatformRole } from "@/lib/api";
import { everyone, hasRole, isAdmin } from "@/lib/roles";
import { lazyPage, type NavItem } from "./types";

const people = () => import("@/pages/People");
const person = () => import("@/pages/Person");
const help = () => import("@/pages/Help");

/** What every part of Grantra shares: home, people, audit, tokens and help. */
export const home: NavItem = {
  path: "/",
  title: "Home",
  description: "Where to start.",
  icon: House,
  module: "platform",
  visible: everyone,
  // Lazy: Home lists these items, so importing it here would be a cycle.
  page: lazyPage(() => import("@/pages/Home"), "Home"),
};

export const platform: NavItem[] = [
  {
    path: "/people",
    title: "People",
    description: "Find anyone and see their access and lifecycle.",
    icon: Users,
    module: "platform",
    group: "governance",
    visible: (me) => hasRole(me, PlatformRole.Admin, PlatformRole.Helpdesk, PlatformRole.Auditor),
    page: lazyPage(people, "People"),
    subpages: [{ path: "/people/:id", page: lazyPage(person, "Person") }],
  },
  {
    path: "/audit",
    title: "Audit log",
    description: "Every change and decision, searchable.",
    icon: FileSearch,
    module: "platform",
    group: "governance",
    visible: (me) => hasRole(me, PlatformRole.Admin, PlatformRole.Auditor),
    page: lazyPage(() => import("@/pages/AuditLog"), "AuditLog"),
  },
  {
    path: "/settings/api-tokens",
    title: "API tokens",
    description: "Service tokens for scripts that send HR exports and account files.",
    icon: KeyRound,
    module: "platform",
    group: "settings",
    visible: isAdmin,
    page: lazyPage(() => import("@/pages/admin/ServiceTokens"), "ServiceTokens"),
    header: true,
  },
];

export const helpItem: NavItem = {
  path: "/help",
  title: "Help",
  description: "How Grantra works, for what you do in it.",
  icon: CircleHelp,
  module: "platform",
  visible: everyone,
  page: lazyPage(help, "Help"),
  subpages: [{ path: "/help/:page", page: lazyPage(help, "HelpArticle") }],
};
