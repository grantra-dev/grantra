import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { Loaded } from "@/components/Loaded";
import { ReadOnlyFields } from "@/components/Manage";
import { Alert } from "@/components/ui/alert";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input, Label, Select } from "@/components/ui/field";
import { useCanManage } from "@/lib/manage";
import {
  listDetections,
  responseLabels,
  saveDetection,
  severities,
  type Detection,
  type ResponseAction,
  type Severity,
} from "@/lib/threats";
import { capitalize } from "@/lib/utils";
import { SeverityBadge } from "./shared";

/** What Grantra looks for, and how each detection is tuned. */
export function Detections() {
  const { data, error, isPending } = useQuery({
    queryKey: ["threat-detections"],
    queryFn: listDetections,
  });
  return (
    <div className="flex flex-col gap-3">
      <p className="text-sm text-slate-500">
        Every detection is on from the start with values that suit most companies. Grantra only acts
        on its own where you pick a response; detections that only see wrong passwords never suspend
        anyone, or anybody could lock a colleague out by typing wrong passwords in their name.
      </p>
      <Loaded loading={isPending} error={error}>
        <ul className="divide-y divide-slate-100">
          {data?.map((d) => (
            <li key={d.id}>
              <DetectionRow detection={d} />
            </li>
          ))}
        </ul>
      </Loaded>
    </div>
  );
}

function DetectionRow({ detection: d }: { detection: Detection }) {
  const client = useQueryClient();
  const canManage = useCanManage();
  const [enabled, setEnabled] = useState(d.enabled);
  const [severity, setSeverity] = useState<Severity>(d.severity);
  const [auto, setAuto] = useState<ResponseAction | "none">(d.auto_response);
  const [values, setValues] = useState<Record<string, number>>(d.values ?? {});
  const save = useMutation({
    mutationFn: () => saveDetection(d.id, { enabled, severity, auto_response: auto, values }),
    onSuccess: () => void client.invalidateQueries({ queryKey: ["threat-detections"] }),
  });
  const changed =
    enabled !== d.enabled ||
    severity !== d.severity ||
    auto !== d.auto_response ||
    (d.params ?? []).some((p) => values[p.name] !== d.values?.[p.name]);
  const autoResponses = d.auto_responses ?? [];
  return (
    <form
      aria-label={d.title}
      className="flex flex-col gap-3 p-4"
      onSubmit={(e) => {
        e.preventDefault();
        save.mutate();
      }}
    >
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div className="max-w-2xl">
          <h3 className="flex flex-wrap items-center gap-2 font-medium">
            {d.title} <SeverityBadge severity={d.severity} />
            {!d.enabled && <Badge>Off</Badge>}
            {d.auto_response !== "none" && (
              <Badge variant="brand">Acts on its own: {responseLabels[d.auto_response]}</Badge>
            )}
          </h3>
          <p className="text-sm text-slate-500">{d.description}</p>
        </div>
        <label className="flex items-center gap-2 text-sm">
          <input
            type="checkbox"
            checked={enabled}
            disabled={!canManage}
            onChange={(e) => setEnabled(e.target.checked)}
          />
          On
        </label>
      </div>
      <ReadOnlyFields>
        <div className="flex flex-wrap gap-3">
          <div>
            <Label htmlFor={`${d.id}-severity`}>Severity</Label>
            <Select
              id={`${d.id}-severity`}
              value={severity}
              onChange={(e) => setSeverity(e.target.value as Severity)}
            >
              {severities.map((s) => (
                <option key={s} value={s}>
                  {capitalize(s)}
                </option>
              ))}
            </Select>
          </div>
          {(d.params ?? []).map((p) => (
            <div key={p.name}>
              <Label htmlFor={`${d.id}-${p.name}`} title={p.description}>
                {p.description}
              </Label>
              <Input
                id={`${d.id}-${p.name}`}
                type="number"
                className="w-28"
                min={p.min}
                max={p.max}
                value={values[p.name] ?? p.default}
                onChange={(e) => setValues({ ...values, [p.name]: Number(e.target.value) })}
              />
            </div>
          ))}
          <div>
            <Label htmlFor={`${d.id}-auto`}>Respond on its own</Label>
            <Select
              id={`${d.id}-auto`}
              value={auto}
              disabled={autoResponses.length === 0}
              onChange={(e) => setAuto(e.target.value as ResponseAction | "none")}
            >
              <option value="none">No, only alert</option>
              {autoResponses.map((r) => (
                <option key={r} value={r}>
                  {responseLabels[r]}
                </option>
              ))}
            </Select>
          </div>
        </div>
      </ReadOnlyFields>
      {canManage && changed && (
        <div>
          <Button type="submit" size="sm" disabled={save.isPending}>
            Save
          </Button>
        </div>
      )}
      {save.error && <Alert variant="error">{save.error.message}</Alert>}
    </form>
  );
}
