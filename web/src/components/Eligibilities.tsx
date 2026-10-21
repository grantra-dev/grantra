import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { Link } from "react-router";
import { Alert, ErrorAlert } from "@/components/ui/alert";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Input, Label, Select } from "@/components/ui/field";
import { listRequestable, targetOf } from "@/lib/api";
import {
  activate,
  deactivate,
  listEligibilities,
  makeEligible,
  removeEligibility,
  type Eligibility,
} from "@/lib/jit";
import { formatDate } from "@/lib/utils";

/**
 * What someone may activate just in time. The person activates and ends
 * their own; admins make others eligible and take it away.
 */
export function Eligibilities({
  identityId,
  self,
  canManage,
}: {
  identityId: string;
  self: boolean;
  canManage: boolean;
}) {
  const { data, error, isPending } = useQuery({
    queryKey: ["eligibilities", identityId],
    queryFn: () => listEligibilities(identityId),
  });
  if (isPending || (!error && data.length === 0 && !canManage)) return null;

  return (
    <Card className="mb-6">
      <CardHeader>
        <CardTitle>Eligible to activate</CardTitle>
        <p className="text-sm text-slate-500">
          {self
            ? "Privileged access you do not hold all the time. Activate it for a few hours when you need it, saying why."
            : "Privileged access they can activate for a few hours when they need it."}
        </p>
      </CardHeader>
      <CardContent className="flex flex-col gap-3">
        {error ? (
          <p role="alert" className="text-sm text-red-700">
            Could not load eligibilities: {error.message}
          </p>
        ) : data.length === 0 ? (
          <p className="text-sm text-slate-500">Not eligible for anything yet.</p>
        ) : (
          <ul className="flex flex-col divide-y divide-slate-200">
            {data.map((el) => (
              <EligibilityRow
                key={el.id}
                el={el}
                identityId={identityId}
                self={self}
                canManage={canManage}
              />
            ))}
          </ul>
        )}
        {canManage && !self && <MakeEligible identityId={identityId} />}
      </CardContent>
    </Card>
  );
}

function EligibilityRow({
  el,
  identityId,
  self,
  canManage,
}: {
  el: Eligibility;
  identityId: string;
  self: boolean;
  canManage: boolean;
}) {
  const client = useQueryClient();
  const [open, setOpen] = useState(false);
  const refresh = () =>
    Promise.all([
      client.invalidateQueries({ queryKey: ["eligibilities", identityId] }),
      client.invalidateQueries({ queryKey: ["identity-access", identityId] }),
    ]);
  const end = useMutation({ mutationFn: () => deactivate(el.id), onSuccess: refresh });
  const remove = useMutation({ mutationFn: () => removeEligibility(el.id), onSuccess: refresh });
  const failed = end.error ?? remove.error;

  return (
    <li className="py-3 first:pt-0 last:pb-0">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div className="min-w-0">
          <div className="flex flex-wrap items-center gap-2">
            <span className="font-medium">{el.name}</span>
            {el.active ? (
              <Badge variant="amber">Active until {formatDate(el.active.expires_at)}</Badge>
            ) : (
              <Badge>Not active</Badge>
            )}
          </div>
          <div className="text-sm text-slate-500">
            {el.type === "role" ? "Role" : el.application || "Entitlement"} · up to{" "}
            {el.max_activation_hours} {el.max_activation_hours === 1 ? "hour" : "hours"}
            {el.activation_approval && " · needs approval"}
            {el.expires_at && <> · eligible until {formatDate(el.expires_at)}</>}
          </div>
          {el.active?.justification && (
            <p className="mt-1 text-sm text-slate-700">“{el.active.justification}”</p>
          )}
        </div>
        <div className="flex gap-2">
          {self && !el.active && !open && (
            <Button size="sm" onClick={() => setOpen(true)}>
              Activate
            </Button>
          )}
          {el.active && (self || canManage) && (
            <Button
              size="sm"
              variant="outline"
              onClick={() => end.mutate()}
              disabled={end.isPending}
            >
              {self ? "Done, end it now" : "End now"}
            </Button>
          )}
          {canManage && !self && (
            <Button
              size="sm"
              variant="outline"
              onClick={() => remove.mutate()}
              disabled={remove.isPending}
            >
              Remove
            </Button>
          )}
        </div>
      </div>
      {open && (
        <ActivateForm
          el={el}
          onDone={() => {
            setOpen(false);
            return refresh();
          }}
          onCancel={() => setOpen(false)}
        />
      )}
      <ErrorAlert error={failed} className="mt-2" />
    </li>
  );
}

function ActivateForm({
  el,
  onDone,
  onCancel,
}: {
  el: Eligibility;
  onDone: () => void;
  onCancel: () => void;
}) {
  const client = useQueryClient();
  const [hours, setHours] = useState(1);
  const [justification, setJustification] = useState("");
  const [sent, setSent] = useState(false);
  const run = useMutation({
    mutationFn: () => activate(el.id, hours, justification.trim()),
    onSuccess: (res) => {
      if (!res.activation) {
        setSent(true);
        void client.invalidateQueries({ queryKey: ["my-requests"] });
      } else onDone();
    },
  });

  if (sent) {
    return (
      <Alert variant="success" className="mt-3">
        Activation sent for approval. It starts once approved; follow it in{" "}
        <Link to="/requests" className="underline">
          My requests
        </Link>
        .
      </Alert>
    );
  }
  return (
    <form
      className="mt-3 grid gap-3 sm:grid-cols-[8rem_1fr_auto] sm:items-end"
      onSubmit={(e) => {
        e.preventDefault();
        run.mutate();
      }}
    >
      <div>
        <Label htmlFor={`hours-${el.id}`}>Hours</Label>
        <Select
          id={`hours-${el.id}`}
          value={hours}
          onChange={(e) => setHours(Number(e.target.value))}
        >
          {Array.from({ length: el.max_activation_hours }, (_, i) => i + 1).map((h) => (
            <option key={h} value={h}>
              {h}
            </option>
          ))}
        </Select>
      </div>
      <div>
        <Label htmlFor={`why-${el.id}`}>Why you need it</Label>
        <Input
          id={`why-${el.id}`}
          value={justification}
          onChange={(e) => setJustification(e.target.value)}
          placeholder="INC-1234: fix the payment run"
          required
        />
      </div>
      <div className="flex gap-2">
        <Button type="submit" disabled={run.isPending || !justification.trim()}>
          {el.activation_approval ? "Ask to activate" : "Activate"}
        </Button>
        <Button type="button" variant="outline" onClick={onCancel}>
          Cancel
        </Button>
      </div>
      <ErrorAlert error={run.error} className="sm:col-span-3" />
    </form>
  );
}

function MakeEligible({ identityId }: { identityId: string }) {
  const client = useQueryClient();
  const [target, setTarget] = useState("");
  const [days, setDays] = useState("");
  const { data: items } = useQuery({ queryKey: ["requestable"], queryFn: listRequestable });
  const jit = (items ?? []).filter((i) => i.jit_only);
  const add = useMutation({
    mutationFn: () => {
      const item = jit.find((i) => `${i.type}:${i.id}` === target)!;
      return makeEligible(identityId, targetOf(item), days ? Number(days) : undefined);
    },
    onSuccess: () => {
      setTarget("");
      setDays("");
      return client.invalidateQueries({ queryKey: ["eligibilities", identityId] });
    },
  });

  return (
    <form
      className="grid gap-3 border-t border-slate-200 pt-3 sm:grid-cols-[1fr_9rem_auto] sm:items-end"
      onSubmit={(e) => {
        e.preventDefault();
        add.mutate();
      }}
    >
      <div>
        <Label htmlFor="eligible-target">Make eligible for</Label>
        <Select id="eligible-target" value={target} onChange={(e) => setTarget(e.target.value)}>
          <option value="">Choose just-in-time access…</option>
          {jit.map((i) => (
            <option key={`${i.type}:${i.id}`} value={`${i.type}:${i.id}`}>
              {i.name}
              {i.application ? ` (${i.application})` : ""}
            </option>
          ))}
        </Select>
      </div>
      <div>
        <Label htmlFor="eligible-days">For days</Label>
        <Input
          id="eligible-days"
          type="number"
          min={1}
          max={365}
          value={days}
          onChange={(e) => setDays(e.target.value)}
          placeholder="No end"
        />
      </div>
      <Button type="submit" disabled={!target || add.isPending}>
        Make eligible
      </Button>
      <ErrorAlert error={add.error} className="sm:col-span-3" />
    </form>
  );
}
