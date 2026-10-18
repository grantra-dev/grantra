import { Fingerprint } from "lucide-react";
import { governs } from "@/lib/roles";
import { lazyPage, type NavItem } from "./types";

/** Grantra Login: the bundled sign-in for companies without their own. */
export const login: NavItem[] = [
  {
    path: "/settings/grantra-login",
    title: "Grantra Login",
    description: "Sign-in policy, passkeys, federation and imports for Grantra Login.",
    icon: Fingerprint,
    module: "login",
    group: "settings",
    visible: governs,
    manages: true,
    page: lazyPage(() => import("@/pages/admin/GrantraLogin"), "GrantraLogin"),
    header: true,
  },
];
