import { Radar } from "lucide-react";
import { governs } from "@/lib/roles";
import { lazyPage, type NavItem } from "./types";

/** Identity threat detection and response. */
export const itdr: NavItem[] = [
  {
    path: "/security/threats",
    title: "Threats",
    description:
      "Suspicious sign-ins and access Grantra found, and what to do about them: end sessions, require a second factor or suspend.",
    icon: Radar,
    module: "itdr",
    group: "security",
    visible: governs,
    manages: true,
    page: lazyPage(() => import("@/pages/Threats"), "Threats"),
    header: true,
    // Open high and critical alerts, for admins.
    count: (inbox) => inbox.threats.length,
  },
];
