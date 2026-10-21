import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ShieldAlert } from "lucide-react";
import { useState } from "react";
import { Loaded } from "@/components/Loaded";
import { ReadOnlyFields } from "@/components/Manage";
import { ErrorAlert } from "@/components/ui/alert";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input, Label } from "@/components/ui/field";
import type { Me } from "@/lib/api";
import { useCanManage } from "@/lib/manage";
import {
  decideSettingChange,
  describeSetting,
  listSettings,
  loosens,
  saveSetting,
  settingGroups,
  type Setting,
  type SettingChange,
} from "@/lib/settings";
import { formatDate } from "@/lib/utils";

/** Settings → General: time limits, reminders and thresholds, by part of Grantra. */
export function GeneralSettings({ me }: { me: Me }) {
  const { data, error, isPending } = useQuery({ queryKey: ["settings"], queryFn: listSettings });
  // Settings of a module the tenant turned off do nothing, so they stay out of the way.
  const shown = (data?.settings ?? []).filter(
    (s) => !s.module || !me.disabled_modules?.includes(s.module),
  );
  return (
    <div className="flex flex-col gap-6">
      <p className="text-sm text-slate-500">
        Every setting starts with a value that suits most companies.{" "}
        {data?.second_admin
          ? "A change that makes Grantra less strict, such as a longer break-glass check-out, waits for another admin to approve it. Stricter changes take effect at once."
          : "With another admin, a change that makes Grantra less strict would wait for them to approve it; as the only admin, your changes take effect at once."}{" "}
        Every change is in the audit log.
      </p>
      <Loaded loading={isPending} error={error}>
        {settingGroups.map((g) => {
          const list = shown.filter((s) => s.group === g.id);
          if (list.length === 0) return null;
          return (
            <section key={g.id} aria-label={g.title}>
              <h2 className="px-4 pt-4 pb-2 text-xs font-semibold tracking-wide text-slate-500 uppercase">
                {g.title}
              </h2>
              <ul className="divide-y divide-slate-100 border-t border-slate-100">
                {list.map((s) => (
                  <li key={s.key}>
                    {/* A new value or change from the server starts the form again. */}
                    <SettingRow
                      key={`${s.value}-${s.pending?.id ?? ""}`}
                      s={s}
                      me={me}
                      secondAdmin={!!data?.second_admin}
                    />
                  </li>
                ))}
              </ul>
            </section>
          );
        })}
      </Loaded>
    </div>
  );
}

function useRefresh() {
  const client = useQueryClient();
  return () =>
    Promise.all([
      client.invalidateQueries({ queryKey: ["settings"] }),
      client.invalidateQueries({ queryKey: ["inbox"] }),
      client.invalidateQueries({ queryKey: ["me"] }),
    ]);
}

function SettingRow({ s, me, secondAdmin }: { s: Setting; me: Me; secondAdmin: boolean }) {
  const canManage = useCanManage();
  const refresh = useRefresh();
  const [value, setValue] = useState(s.value);
  const [reason, setReason] = useState("");
  const save = useMutation({
    mutationFn: () => saveSetting(s.key, value, reason),
    onSuccess: async () => {
      setReason("");
      await refresh();
    },
  });
  const id = `setting-${s.key}`;
  const changed = value !== s.value;
  const waits = changed && secondAdmin && loosens(s, s.value, value);
  const valid =
    s.kind === "switch" || (Number.isInteger(value) && value >= s.min && value <= s.max);
  return (
    <form
      aria-label={s.title}
      className="flex flex-col gap-3 p-4"
      onSubmit={(e) => {
        e.preventDefault();
        save.mutate();
      }}
    >
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div className="max-w-2xl">
          <h3 className="flex flex-wrap items-center gap-2 font-medium">
            <label htmlFor={id}>{s.title}</label>
            {s.value === s.default ? (
              <Badge>Default</Badge>
            ) : (
              <Badge variant="brand">
                Changed{s.updated_at ? ` ${formatDate(s.updated_at)}` : ""} · default{" "}
                {describeSetting(s, s.default)}
              </Badge>
            )}
          </h3>
          <p className="text-sm text-slate-500">{s.help}</p>
          {s.looser && (
            <p className="mt-1 flex items-center gap-1 text-xs text-slate-500">
              <ShieldAlert className="size-3.5" aria-hidden />
              {s.kind === "switch"
                ? "Turning it on makes Grantra less strict."
                : `A ${s.looser} value makes Grantra less strict.`}
            </p>
          )}
        </div>
        <ReadOnlyFields>
          {s.kind === "switch" ? (
            <input
              id={id}
              type="checkbox"
              role="switch"
              className="mt-1 size-5 accent-brand-600"
              checked={value === 1}
              disabled={!!s.pending}
              onChange={(e) => setValue(e.target.checked ? 1 : 0)}
            />
          ) : (
            <span className="flex items-center gap-2 text-sm text-slate-600">
              <Input
                id={id}
                type="number"
                className="w-28"
                min={s.min}
                max={s.max}
                value={Number.isNaN(value) ? "" : value}
                disabled={!!s.pending}
                onChange={(e) => setValue(e.target.valueAsNumber)}
              />
              <span className="w-20">{s.unit === "percent" ? "%" : s.unit}</span>
            </span>
          )}
        </ReadOnlyFields>
      </div>
      {s.pending && <PendingChange c={s.pending} setting={s} me={me} />}
      {canManage && changed && !s.pending && (
        <div className="flex flex-col gap-2">
          {s.kind === "number" && !valid && (
            <p className="text-sm text-red-700">
              Between {s.min} and {s.max} {s.unit === "percent" ? "%" : s.unit}.
            </p>
          )}
          {waits && (
            <div>
              <Label htmlFor={`${id}-reason`}>Why (for the admin who approves it)</Label>
              <Input
                id={`${id}-reason`}
                value={reason}
                maxLength={2000}
                placeholder="CHG-1234: night shift needs longer check-outs"
                onChange={(e) => setReason(e.target.value)}
              />
            </div>
          )}
          <div className="flex items-center gap-2">
            <Button type="submit" size="sm" disabled={save.isPending || !valid}>
              {waits ? "Ask another admin" : "Save"}
            </Button>
            <Button type="button" size="sm" variant="ghost" onClick={() => setValue(s.value)}>
              Cancel
            </Button>
            {value !== s.default && (
              <Button type="button" size="sm" variant="ghost" onClick={() => setValue(s.default)}>
                Use the default ({describeSetting(s, s.default)})
              </Button>
            )}
          </div>
        </div>
      )}
      <ErrorAlert error={save.error} />
    </form>
  );
}

/** A change waiting for a second admin: they approve or reject it; whoever proposed it may withdraw it. */
export function PendingChange({
  c,
  setting,
  me,
}: {
  c: SettingChange;
  setting: Pick<Setting, "kind" | "unit">;
  me: Me;
}) {
  const canManage = useCanManage();
  const refresh = useRefresh();
  const decide = useMutation({
    mutationFn: (approve: boolean) => decideSettingChange(c.id, approve),
    onSuccess: refresh,
  });
  const mine = c.proposed_by === `identity:${me.identity_id}`;
  return (
    <div className="rounded-md border border-amber-200 bg-amber-50 p-3 text-sm">
      <p>
        <span className="font-medium">{mine ? "You" : (c.proposed_by_name ?? "An admin")}</span>{" "}
        asked to change it from {describeSetting(setting, c.old_value)} to{" "}
        <span className="font-medium">{describeSetting(setting, c.value)}</span> on{" "}
        {formatDate(c.proposed_at)}.{" "}
        {mine ? "It waits for another admin." : "It waits for you, or another admin."}
      </p>
      {c.reason && <p className="mt-1 text-slate-600">Why: {c.reason}</p>}
      {canManage && (
        <div className="mt-2 flex gap-2">
          {mine ? (
            <Button
              type="button"
              size="sm"
              variant="outline"
              disabled={decide.isPending}
              onClick={() => decide.mutate(false)}
            >
              Withdraw
            </Button>
          ) : (
            <>
              <Button
                type="button"
                size="sm"
                disabled={decide.isPending}
                onClick={() => decide.mutate(true)}
              >
                Approve
              </Button>
              <Button
                type="button"
                size="sm"
                variant="outline"
                disabled={decide.isPending}
                onClick={() => decide.mutate(false)}
              >
                Reject
              </Button>
            </>
          )}
        </div>
      )}
      <ErrorAlert error={decide.error} className="mt-2" />
    </div>
  );
}
