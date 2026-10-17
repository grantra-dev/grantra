import {
  ClipboardCheck,
  Database,
  FileSearch,
  House,
  Inbox,
  KeyRound,
  ListChecks,
  LockKeyhole,
  type LucideIcon,
  Settings,
  ShoppingBag,
  SquareTerminal,
  Users,
} from "lucide-react";
import { PlatformRole, type Me } from "./api";

export interface Section {
  path: string;
  title: string;
  description: string;
  icon: LucideIcon;
  /** Whether this person sees the section. The API still checks every call. */
  visible: (me: Me) => boolean;
  /** The milestone issue that builds the page, while it is still a placeholder. */
  issue?: number;
}

const hasRole = (me: Me, ...roles: string[]) => roles.some((r) => me.platform_roles.includes(r));

const everyone = () => true;

// Managers and application owners approve requests and review access; admins
// run the campaigns.
const approves = (me: Me) =>
  me.manages_people || me.owns_applications || hasRole(me, PlatformRole.Admin);

// Sections follow docs/console.md: one app, and each person sees what their
// relationships (manager, application owner) and platform roles give them.
export const sections: Section[] = [
  {
    path: "/",
    title: "Home",
    description: "Where to start.",
    icon: House,
    visible: everyone,
  },
  {
    path: "/my-access",
    title: "My access",
    description: "What you can access, why, and until when.",
    icon: KeyRound,
    visible: everyone,
  },
  {
    path: "/request",
    title: "Request access",
    description: "Ask for a role or entitlement from the catalog.",
    icon: ShoppingBag,
    visible: everyone,
  },
  {
    path: "/requests",
    title: "My requests",
    description: "Follow what you asked for, and withdraw it while it is pending.",
    icon: Inbox,
    visible: everyone,
  },
  {
    path: "/vault",
    title: "Vault",
    description: "Check out the privileged accounts you hold access to.",
    icon: LockKeyhole,
    visible: everyone,
  },
  {
    path: "/servers",
    title: "Servers",
    description: "Log in to servers over SSH with a short-lived certificate.",
    icon: SquareTerminal,
    visible: everyone,
  },
  {
    path: "/databases",
    title: "Databases & clusters",
    description: "Connect to databases and Kubernetes clusters with a short-lived token.",
    icon: Database,
    visible: everyone,
  },
  {
    path: "/approvals",
    title: "Approvals",
    description: "Access requests waiting for your decision.",
    icon: ClipboardCheck,
    visible: approves,
  },
  {
    path: "/reviews",
    title: "Access reviews",
    description: "Keep or revoke the access you are asked to review.",
    icon: ListChecks,
    visible: (me) => approves(me) || hasRole(me, PlatformRole.Auditor),
  },
  {
    path: "/people",
    title: "People",
    description: "Find anyone and see their access and lifecycle.",
    icon: Users,
    visible: (me) => hasRole(me, PlatformRole.Admin, PlatformRole.Helpdesk, PlatformRole.Auditor),
  },
  {
    path: "/admin",
    title: "Administration",
    description: "HR sources, catalog, connectors, campaigns and policies.",
    icon: Settings,
    visible: (me) => hasRole(me, PlatformRole.Admin),
  },
  {
    path: "/audit",
    title: "Audit log",
    description: "Every change and decision, searchable.",
    icon: FileSearch,
    visible: (me) => hasRole(me, PlatformRole.Admin, PlatformRole.Auditor),
  },
];

export const visibleSections = (me: Me) => sections.filter((s) => s.visible(me));
