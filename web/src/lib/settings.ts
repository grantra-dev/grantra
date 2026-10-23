// How the tenant has Grantra set up (Settings → General): time limits,
// reminders and thresholds, each with its default and limits.
import { useQuery } from "@tanstack/react-query";
import { api, send, type Me } from "./api";

export type SettingGroup =
  "requests" | "reviews" | "sod" | "pam" | "threats" | "insights" | "reports" | "console";

/** The groups, in the order the page shows them. */
export const settingGroups: { id: SettingGroup; title: string }[] = [
  { id: "requests", title: "Access requests" },
  { id: "reviews", title: "Access reviews" },
  { id: "sod", title: "Separation of duties" },
  { id: "pam", title: "Privileged access" },
  { id: "threats", title: "Threat detection" },
  { id: "insights", title: "Insights" },
  { id: "reports", title: "Compliance reports" },
  { id: "console", title: "Console sign-in" },
];

/** A change that makes Grantra less strict, waiting for a second admin. */
export interface SettingChange {
  id: string;
  key: string;
  title: string;
  unit?: string;
  kind: "number" | "switch";
  old_value: number;
  value: number;
  reason?: string;
  proposed_by: string;
  proposed_by_name?: string;
  proposed_at: string;
  status: "pending" | "approved" | "rejected" | "withdrawn";
}

export interface Setting {
  key: string;
  group: SettingGroup;
  module?: string;
  title: string;
  help: string;
  kind: "number" | "switch";
  unit?: string;
  default: number;
  min: number;
  max: number;
  /** Which way the setting makes Grantra less strict; such changes wait for a second admin. */
  looser?: "higher" | "lower";
  value: number;
  updated_by?: string;
  updated_at?: string;
  pending?: SettingChange;
}

export interface SettingsPage {
  settings: Setting[];
  /** Whether a less strict change by you waits for another admin. */
  second_admin: boolean;
}

export const listSettings = () => api<SettingsPage>("/api/v1/settings");

export const saveSetting = (key: string, value: number, reason: string) =>
  send<Setting>("PUT", `/api/v1/settings/${encodeURIComponent(key)}`, { value, reason });

export const decideSettingChange = (id: string, approve: boolean, comment = "") =>
  send<SettingChange>("POST", `/api/v1/setting-changes/${encodeURIComponent(id)}/decision`, {
    approve,
    comment,
  });

/** Whether going from old to value makes Grantra less strict. */
export const loosens = (s: Pick<Setting, "looser">, old: number, value: number) =>
  (s.looser === "higher" && value > old) || (s.looser === "lower" && value < old);

/** A value the way people read it: "60 minutes", "75%", "on". */
export function describeSetting(s: Pick<Setting, "kind" | "unit">, v: number) {
  if (s.kind === "switch") return v === 1 ? "on" : "off";
  if (s.unit === "percent") return `${v}%`;
  if (!s.unit) return String(v);
  // "1 hours" reads badly.
  return `${v} ${v === 1 ? s.unit.replace(/s$/, "") : s.unit}`;
}

/** Keys of the settings the console reads to explain or prefill what it shows. */
export const SettingKey = {
  ReviewDefaultDueDays: "reviews.default_due_days",
  SoDExceptionMaxDays: "sod.exception_max_days",
  SoDExceptionsWithoutEnd: "sod.exceptions_without_end",
  UnusedDays: "insights.unused_days",
} as const;

/**
 * The tenant's value of a setting, for the admins and auditors who may read
 * settings; undefined until it loads, or for anyone else.
 */
export function useSetting(key: string, enabled = true) {
  const { data } = useQuery({ queryKey: ["settings"], queryFn: listSettings, enabled });
  return data?.settings.find((s) => s.key === key)?.value;
}

/** How long an accepted separation of duties risk can last, as offered in the console. */
const exceptionDays = [7, 30, 90, 180, 365];

/**
 * The lengths to offer for an accepted risk: none longer than the tenant
 * allows (most), which is offered itself when it falls between, and extra
 * when given (such as the access's own duration).
 */
export function exceptionChoices(most = 365, extra?: number) {
  const days = exceptionDays.filter((d) => d <= most);
  for (const d of [most, extra]) {
    if (d && d <= most && !days.includes(d)) days.push(d);
  }
  return days.sort((a, b) => a - b);
}

/** How long access goes unused before it counts as unused, which /me gives everyone. */
export const unusedDays = (me?: Me) => me?.settings?.[SettingKey.UnusedDays];
