import { LockKeyhole, MonitorCloud, SquareTerminal } from "lucide-react";
import { everyone } from "@/lib/roles";
import { lazyPage, type NavItem } from "./types";

/** Privileged access: the vault, servers and the gateways. */
export const pam: NavItem[] = [
  {
    path: "/vault",
    title: "Vault",
    description: "Check out the privileged accounts you hold access to.",
    icon: LockKeyhole,
    module: "pam",
    group: "privileged",
    visible: everyone,
    page: lazyPage(() => import("@/pages/Vault"), "Vault"),
    // Break-glass uses waiting for an admin's review.
    count: (inbox) => inbox.break_glass.length,
  },
  {
    path: "/servers",
    title: "Servers",
    description: "Log in to servers over SSH with a short-lived certificate.",
    icon: SquareTerminal,
    module: "pam",
    group: "privileged",
    visible: everyone,
    page: lazyPage(() => import("@/pages/Servers"), "Servers"),
    subpages: [
      {
        path: "/servers/sessions/:id",
        page: lazyPage(() => import("@/pages/SessionPlayback"), "SessionPlayback"),
      },
    ],
  },
  {
    path: "/remote-access",
    title: "Remote access",
    description:
      "Connect to databases, clusters, desktops and cloud consoles for as long as your access lasts.",
    icon: MonitorCloud,
    module: "pam",
    group: "privileged",
    visible: everyone,
    page: lazyPage(() => import("@/pages/RemoteAccess"), "RemoteAccess"),
    subpages: [
      {
        path: "/remote-access/sessions/:id",
        page: lazyPage(() => import("@/pages/GatewaySessionLog"), "GatewaySessionLog"),
      },
    ],
  },
];
