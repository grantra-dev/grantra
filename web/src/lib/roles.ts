import { PlatformRole, type Me } from "./api";

export const hasRole = (me: Me, ...roles: string[]) =>
  roles.some((r) => me.platform_roles.includes(r));

export const everyone = () => true;

// Managers and application owners approve requests and review access; admins
// run the campaigns.
export const approves = (me: Me) =>
  me.manages_people || me.owns_applications || hasRole(me, PlatformRole.Admin);

export const isAdmin = (me: Me) => hasRole(me, PlatformRole.Admin);

// Governance and Settings are for the people who run Grantra: admins change
// things there, auditors look at them.
export const governs = (me: Me) => hasRole(me, PlatformRole.Admin, PlatformRole.Auditor);
