import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Search, X } from "lucide-react";
import { useState } from "react";
import { useNavigate } from "react-router";
import { PageHeader } from "@/components/PageHeader";
import { RiskBadge } from "@/components/RiskBadge";
import { Alert } from "@/components/ui/alert";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Label, Select, Textarea } from "@/components/ui/field";
import {
  createRequest,
  getIdentityAccess,
  listMyRequests,
  listRequestable,
  previewRequest,
  targetOf,
  type Me,
  type Requestable,
} from "@/lib/api";

export function RequestAccess({ me }: { me: Me }) {
  const [filter, setFilter] = useState("");
  const [selected, setSelected] = useState<Requestable | null>(null);
  const catalog = useQuery({ queryKey: ["requestable"], queryFn: listRequestable });
  const access = useQuery({
    queryKey: ["identity-access", me.identity_id],
    queryFn: () => getIdentityAccess(me.identity_id),
  });
  const mine = useQuery({ queryKey: ["my-requests"], queryFn: listMyRequests });

  const held = new Set(access.data?.map((a) => a.target_id));
  const pending = new Set(
    mine.data
      ?.filter((r) => r.status === "pending" && r.beneficiary_id === me.identity_id)
      .map((r) => r.target_id),
  );

  const needle = filter.trim().toLowerCase();
  const items = (catalog.data ?? []).filter(
    (i) =>
      !needle ||
      [i.name, i.application, i.description].some((v) => v?.toLowerCase().includes(needle)),
  );

  return (
    <>
      <PageHeader
        title="Request access"
        description="Find what you need and say why. Your manager and the owner approve it."
      />
      <div className="grid items-start gap-6 lg:grid-cols-[minmax(0,1fr)_24rem]">
        <Card>
          <div className="border-b border-slate-200 p-4">
            <label className="relative block max-w-sm">
              <span className="sr-only">Search the catalog</span>
              <Search
                className="pointer-events-none absolute top-2.5 left-3 size-4 text-slate-400"
                aria-hidden
              />
              <input
                type="search"
                value={filter}
                onChange={(e) => setFilter(e.target.value)}
                placeholder="Search by name, application or description"
                className="h-9 w-full rounded-md border border-slate-300 pr-3 pl-9 text-sm focus:outline-2 focus:outline-brand-600"
              />
            </label>
          </div>
          {catalog.isPending ? (
            <p className="p-6 text-sm text-slate-500">Loading the catalog…</p>
          ) : catalog.error ? (
            <p role="alert" className="p-6 text-sm text-red-700">
              Could not load the catalog: {catalog.error.message}
            </p>
          ) : items.length === 0 ? (
            <p className="p-6 text-sm text-slate-500">
              {needle
                ? `Nothing matches “${filter}”.`
                : "Nothing can be requested yet. Your admins add applications to the catalog."}
            </p>
          ) : (
            <ul className="divide-y divide-slate-100">
              {items.map((i) => (
                <li key={i.id} className="flex items-start justify-between gap-4 p-4">
                  <div className="min-w-0">
                    <div className="flex flex-wrap items-center gap-2">
                      <span className="font-medium">{i.name}</span>
                      <RiskBadge risk={i.risk} />
                    </div>
                    <div className="text-sm text-slate-500">
                      {i.type === "role" ? "Role" : i.application}
                    </div>
                    {i.description && (
                      <p className="mt-1 text-sm text-slate-600">{i.description}</p>
                    )}
                  </div>
                  {held.has(i.id) ? (
                    <Badge variant="green">You have this</Badge>
                  ) : pending.has(i.id) ? (
                    <Badge variant="amber">Requested</Badge>
                  ) : (
                    <Button
                      size="sm"
                      variant={selected?.id === i.id ? "default" : "outline"}
                      onClick={() => setSelected(i)}
                      aria-label={`Request ${i.name}`}
                    >
                      Request
                    </Button>
                  )}
                </li>
              ))}
            </ul>
          )}
        </Card>
        {selected ? (
          <RequestForm key={selected.id} item={selected} onClose={() => setSelected(null)} />
        ) : (
          <Card className="hidden lg:block">
            <CardContent className="pt-5 text-sm text-slate-500">
              Pick something from the catalog to request it.
            </CardContent>
          </Card>
        )}
      </div>
    </>
  );
}

const durations = [
  { days: 0, label: "Until it is removed" },
  { days: 7, label: "1 week" },
  { days: 30, label: "30 days" },
  { days: 90, label: "90 days" },
  { days: 180, label: "6 months" },
  { days: 365, label: "1 year" },
];

function RequestForm({ item, onClose }: { item: Requestable; onClose: () => void }) {
  const [justification, setJustification] = useState("");
  const [days, setDays] = useState(0);
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const target = targetOf(item);

  const check = useQuery({
    queryKey: ["request-preview", item.id],
    queryFn: () => previewRequest(target),
  });
  const conflicts = check.data ?? [];

  const submit = useMutation({
    mutationFn: () =>
      createRequest({
        ...target,
        justification: justification.trim(),
        duration_days: days || undefined,
      }),
    onSuccess: async (req) => {
      await queryClient.invalidateQueries({ queryKey: ["my-requests"] });
      navigate("/requests", { state: { created: req.target_name } });
    },
  });

  const blocked = check.isPending;
  return (
    <Card className="lg:sticky lg:top-5">
      <CardHeader className="flex-row items-start justify-between">
        <div>
          <CardTitle>Request {item.name}</CardTitle>
          <CardDescription>{item.type === "role" ? "Role" : item.application}</CardDescription>
        </div>
        <Button variant="ghost" size="sm" onClick={onClose} aria-label="Close">
          <X />
        </Button>
      </CardHeader>
      <CardContent>
        <form
          className="flex flex-col gap-4"
          onSubmit={(e) => {
            e.preventDefault();
            submit.mutate();
          }}
        >
          {check.error && (
            <Alert variant="error">
              Could not check separation of duties: {check.error.message}
            </Alert>
          )}
          {conflicts.length > 0 && (
            <Alert variant="warning">
              Together with what you already have, this access breaks the separation of duties{" "}
              {conflicts.length === 1 ? "policy" : "policies"}{" "}
              <strong>{conflicts.join(", ")}</strong>. You can still send the request: after your
              approvers, a Grantra admin has to accept the risk, with a reason and an end date, or
              the request is rejected. Explain why you need both.
            </Alert>
          )}
          <div>
            <Label htmlFor="justification">Why do you need it?</Label>
            <Textarea
              id="justification"
              required
              maxLength={2000}
              value={justification}
              onChange={(e) => setJustification(e.target.value)}
              placeholder="For example: I am joining the Q3 billing project"
            />
            <p className="mt-1 text-xs text-slate-500">
              Your approvers and the auditors read this.
            </p>
          </div>
          <div>
            <Label htmlFor="duration">For how long?</Label>
            <Select id="duration" value={days} onChange={(e) => setDays(Number(e.target.value))}>
              {durations.map((d) => (
                <option key={d.days} value={d.days}>
                  {d.label}
                </option>
              ))}
            </Select>
          </div>
          {submit.error && <Alert variant="error">{submit.error.message}</Alert>}
          <Button type="submit" disabled={blocked || !justification.trim() || submit.isPending}>
            {submit.isPending ? "Sending…" : "Send request"}
          </Button>
        </form>
      </CardContent>
    </Card>
  );
}
