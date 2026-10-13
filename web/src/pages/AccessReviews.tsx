import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { useSearchParams } from "react-router";
import { PageHeader } from "@/components/PageHeader";
import { RiskBadge } from "@/components/RiskBadge";
import { Alert } from "@/components/ui/alert";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Label, Select } from "@/components/ui/field";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import {
  closeCampaign,
  decideReviewItem,
  launchCampaign,
  listApplications,
  listCampaigns,
  listMyReviewItems,
  PlatformRole,
  type Campaign,
  type Me,
  type NewCampaign,
  type ReviewItem,
} from "@/lib/api";
import { cn, formatDate } from "@/lib/utils";

export function AccessReviews({ me }: { me: Me }) {
  const isAdmin = me.platform_roles.includes(PlatformRole.Admin);
  const seesCampaigns = isAdmin || me.platform_roles.includes(PlatformRole.Auditor);
  const [params, setParams] = useSearchParams();
  const tab = seesCampaigns && params.get("tab") === "campaigns" ? "campaigns" : "review";

  return (
    <>
      <PageHeader
        title="Access reviews"
        description="Confirm that people still need the access they have, or take it away."
      />
      {seesCampaigns && (
        <div role="tablist" className="mb-4 flex gap-1 border-b border-slate-200">
          {(
            [
              ["review", "To review"],
              ["campaigns", "Campaigns"],
            ] as const
          ).map(([key, label]) => (
            <button
              key={key}
              role="tab"
              aria-selected={tab === key}
              onClick={() => setParams(key === "review" ? {} : { tab: key })}
              className={cn(
                "-mb-px border-b-2 px-3 py-2 text-sm",
                tab === key
                  ? "border-brand-600 font-medium text-brand-700"
                  : "border-transparent text-slate-500 hover:text-slate-900",
              )}
            >
              {label}
            </button>
          ))}
        </div>
      )}
      {tab === "review" ? <ToReview /> : <Campaigns canManage={isAdmin} />}
    </>
  );
}

const originLabels = {
  request: "Requested",
  manual: "Granted by an admin",
  birthright: "Birthright",
};

function ToReview() {
  const queryClient = useQueryClient();
  const [selected, setSelected] = useState<Set<string>>(new Set());
  const [comment, setComment] = useState("");
  const [confirmRevoke, setConfirmRevoke] = useState(false);
  const [failures, setFailures] = useState<string[]>([]);
  const [done, setDone] = useState<string | null>(null);
  const { data, error, isPending } = useQuery({
    queryKey: ["review-items"],
    queryFn: listMyReviewItems,
  });

  const decide = useMutation({
    mutationFn: async ({
      items,
      decision,
    }: {
      items: ReviewItem[];
      decision: "keep" | "revoke";
    }) => {
      const failed: string[] = [];
      for (const it of items) {
        try {
          await decideReviewItem(it.id, decision, comment.trim());
        } catch (e) {
          failed.push(`${it.identity_name} · ${it.target_name}: ${(e as Error).message}`);
        }
      }
      return { failed, count: items.length - failed.length, decision };
    },
    onSuccess: async ({ failed, count, decision }) => {
      setFailures(failed);
      setDone(
        count === 0
          ? null
          : `${decision === "keep" ? "Kept" : "Revoked"} ${count} ${count === 1 ? "access" : "accesses"}.`,
      );
      setSelected(new Set());
      setConfirmRevoke(false);
      // A comment explains one round of decisions, not the next one.
      if (count > 0) setComment("");
      await queryClient.invalidateQueries({ queryKey: ["review-items"] });
    },
  });

  if (isPending) return <p className="text-sm text-slate-500">Loading what you review…</p>;
  if (error) {
    return (
      <p role="alert" className="text-sm text-red-700">
        Could not load your reviews: {error.message}
      </p>
    );
  }

  const chosen = data.filter((it) => selected.has(it.id));
  const toggle = (id: string) =>
    setSelected((prev) => {
      const next = new Set(prev);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      return next;
    });
  const campaigns = [...new Set(data.map((it) => it.campaign_id))];

  return (
    <div className="flex flex-col gap-4">
      {done && <Alert variant="success">{done}</Alert>}
      {failures.length > 0 && (
        <Alert variant="error">
          Some decisions failed:
          <ul className="mt-1 list-disc pl-5">
            {failures.map((f) => (
              <li key={f}>{f}</li>
            ))}
          </ul>
        </Alert>
      )}
      {data.length === 0 ? (
        <Card className="p-6 text-sm text-slate-500">Nothing to review right now. 🎉</Card>
      ) : (
        <>
          <Card className="flex flex-wrap items-end gap-3 p-4">
            <div className="min-w-48 flex-1">
              <Label htmlFor="review-comment">Comment for your decisions</Label>
              <input
                id="review-comment"
                value={comment}
                maxLength={2000}
                onChange={(e) => setComment(e.target.value)}
                placeholder="Optional, for example: left the project in September"
                className="h-9 w-full rounded-md border border-slate-300 px-3 text-sm focus:outline-2 focus:outline-brand-600"
              />
            </div>
            <Button
              variant="outline"
              disabled={chosen.length === 0 || decide.isPending}
              onClick={() => decide.mutate({ items: chosen, decision: "keep" })}
            >
              Keep selected ({chosen.length})
            </Button>
            {confirmRevoke ? (
              <Button
                className="bg-red-600 hover:bg-red-700"
                disabled={chosen.length === 0 || decide.isPending}
                onClick={() => decide.mutate({ items: chosen, decision: "revoke" })}
              >
                Confirm: revoke {chosen.length}
              </Button>
            ) : (
              <Button
                variant="outline"
                disabled={chosen.length === 0 || decide.isPending}
                onClick={() => setConfirmRevoke(true)}
              >
                Revoke selected ({chosen.length})
              </Button>
            )}
          </Card>
          {campaigns.map((cid) => {
            const items = data.filter((it) => it.campaign_id === cid);
            const allChosen = items.every((it) => selected.has(it.id));
            return (
              <Card key={cid}>
                <CardHeader className="flex-row items-center justify-between border-b border-slate-200">
                  <CardTitle>{items[0]!.campaign_name}</CardTitle>
                  <span className="text-sm text-slate-500">{items.length} to review</span>
                </CardHeader>
                <Table>
                  <TableHeader>
                    <TableRow>
                      <TableHead className="w-10">
                        <input
                          type="checkbox"
                          aria-label={`Select all in ${items[0]!.campaign_name}`}
                          checked={allChosen}
                          onChange={() =>
                            setSelected((prev) => {
                              const next = new Set(prev);
                              for (const it of items) {
                                if (allChosen) next.delete(it.id);
                                else next.add(it.id);
                              }
                              return next;
                            })
                          }
                        />
                      </TableHead>
                      <TableHead>Person</TableHead>
                      <TableHead>Access</TableHead>
                      <TableHead>How they got it</TableHead>
                      <TableHead className="text-right">Decision</TableHead>
                    </TableRow>
                  </TableHeader>
                  <TableBody>
                    {items.map((it) => (
                      <TableRow key={it.id}>
                        <TableCell>
                          <input
                            type="checkbox"
                            aria-label={`Select ${it.identity_name}: ${it.target_name}`}
                            checked={selected.has(it.id)}
                            onChange={() => toggle(it.id)}
                          />
                        </TableCell>
                        <TableCell className="font-medium">{it.identity_name}</TableCell>
                        <TableCell>
                          <div className="flex flex-wrap items-center gap-2">
                            {it.target_name}
                            <RiskBadge risk={it.risk} />
                          </div>
                          <div className="text-slate-500">
                            {it.type === "role" ? "Role" : it.application}
                          </div>
                        </TableCell>
                        <TableCell>
                          {originLabels[it.origin]}
                          <div className="text-slate-500">since {formatDate(it.granted_at)}</div>
                        </TableCell>
                        <TableCell className="text-right whitespace-nowrap">
                          <Button
                            size="sm"
                            variant="outline"
                            disabled={decide.isPending}
                            onClick={() => decide.mutate({ items: [it], decision: "keep" })}
                            aria-label={`Keep ${it.identity_name}: ${it.target_name}`}
                          >
                            Keep
                          </Button>{" "}
                          <Button
                            size="sm"
                            variant="outline"
                            className="text-red-700"
                            disabled={decide.isPending}
                            onClick={() => decide.mutate({ items: [it], decision: "revoke" })}
                            aria-label={`Revoke ${it.identity_name}: ${it.target_name}`}
                          >
                            Revoke
                          </Button>
                        </TableCell>
                      </TableRow>
                    ))}
                  </TableBody>
                </Table>
              </Card>
            );
          })}
          <p className="text-xs text-slate-500">
            Revoking removes the access right away. If the application is provisioned, Grantra also
            takes it out of the application.
          </p>
        </>
      )}
    </div>
  );
}

function Campaigns({ canManage }: { canManage: boolean }) {
  const [creating, setCreating] = useState(false);
  const { data, error, isPending } = useQuery({
    queryKey: ["campaigns"],
    queryFn: listCampaigns,
  });

  return (
    <div className="flex flex-col gap-4">
      {canManage &&
        (creating ? (
          <NewCampaignForm onDone={() => setCreating(false)} />
        ) : (
          <div>
            <Button onClick={() => setCreating(true)}>New campaign</Button>
          </div>
        ))}
      {isPending ? (
        <p className="text-sm text-slate-500">Loading campaigns…</p>
      ) : error ? (
        <p role="alert" className="text-sm text-red-700">
          Could not load campaigns: {error.message}
        </p>
      ) : data.length === 0 ? (
        <Card className="p-6 text-sm text-slate-500">No campaigns yet.</Card>
      ) : (
        data.map((c) => <CampaignCard key={c.id} c={c} canManage={canManage} />)
      )}
    </div>
  );
}

function CampaignCard({ c, canManage }: { c: Campaign; canManage: boolean }) {
  const queryClient = useQueryClient();
  const [confirm, setConfirm] = useState(false);
  const close = useMutation({
    mutationFn: () => closeCampaign(c.id),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ["campaigns"] }),
  });
  const keep = c.progress.keep ?? 0;
  const revoke = c.progress.revoke ?? 0;
  const pending = c.progress.pending ?? 0;
  const total = keep + revoke + pending;
  const pct = total === 0 ? 100 : Math.round(((keep + revoke) / total) * 100);

  return (
    <Card className="p-4">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div>
          <div className="flex items-center gap-2">
            <span className="font-medium">{c.name}</span>
            {c.status === "active" ? (
              <Badge variant="amber">Active</Badge>
            ) : (
              <Badge variant="green">Completed</Badge>
            )}
          </div>
          <div className="text-sm text-slate-500">
            Reviewed by {c.reviewer === "manager" ? "managers" : "owners"} ·{" "}
            {c.application_ids.length === 0
              ? "all access"
              : `${c.application_ids.length} ${c.application_ids.length === 1 ? "application" : "applications"}`}{" "}
            · {c.status === "active" ? "due" : "closed"}{" "}
            {formatDate(c.status === "active" ? c.due_at : (c.closed_at ?? c.due_at))} · unreviewed
            access is {c.on_missing === "keep" ? "kept" : "revoked"}
          </div>
        </div>
        {canManage &&
          c.status === "active" &&
          (confirm ? (
            <Button
              size="sm"
              className="bg-red-600 hover:bg-red-700"
              disabled={close.isPending}
              onClick={() => close.mutate()}
            >
              Confirm: close now
            </Button>
          ) : (
            <Button size="sm" variant="outline" onClick={() => setConfirm(true)}>
              Close campaign
            </Button>
          ))}
      </div>
      <div className="mt-3">
        <div
          className="h-2 overflow-hidden rounded-full bg-slate-100"
          role="progressbar"
          aria-label={`${c.name} progress`}
          aria-valuenow={pct}
          aria-valuemin={0}
          aria-valuemax={100}
        >
          <div className="h-full bg-brand-600" style={{ width: `${pct}%` }} />
        </div>
        <div className="mt-1 text-sm text-slate-500">
          {keep + revoke} of {total} reviewed · {keep} kept · {revoke} revoked
        </div>
      </div>
      {confirm && !close.isPending && (
        <p className="mt-2 text-sm text-slate-600">
          Closing ends the campaign now. The {pending} items still pending will be{" "}
          {c.on_missing === "keep" ? "kept" : "revoked"}.
        </p>
      )}
      {close.error && (
        <Alert variant="error" className="mt-3">
          {close.error.message}
        </Alert>
      )}
    </Card>
  );
}

function NewCampaignForm({ onDone }: { onDone: () => void }) {
  const queryClient = useQueryClient();
  const apps = useQuery({ queryKey: ["applications"], queryFn: listApplications });
  const [form, setForm] = useState<NewCampaign>({
    name: "",
    reviewer: "manager",
    application_ids: [],
    on_missing: "keep",
    due_in_days: 14,
  });
  const launch = useMutation({
    mutationFn: () => launchCampaign({ ...form, name: form.name.trim() }),
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: ["campaigns"] });
      await queryClient.invalidateQueries({ queryKey: ["review-items"] });
      onDone();
    },
  });
  const toggleApp = (id: string) =>
    setForm((f) => ({
      ...f,
      application_ids: f.application_ids.includes(id)
        ? f.application_ids.filter((a) => a !== id)
        : [...f.application_ids, id],
    }));

  return (
    <Card>
      <CardHeader>
        <CardTitle>New campaign</CardTitle>
      </CardHeader>
      <CardContent>
        <form
          className="grid gap-4 sm:grid-cols-2"
          onSubmit={(e) => {
            e.preventDefault();
            launch.mutate();
          }}
        >
          <div className="sm:col-span-2">
            <Label htmlFor="campaign-name">Name</Label>
            <input
              id="campaign-name"
              required
              maxLength={200}
              value={form.name}
              onChange={(e) => setForm({ ...form, name: e.target.value })}
              placeholder="Q4 access review"
              className="h-9 w-full rounded-md border border-slate-300 px-3 text-sm focus:outline-2 focus:outline-brand-600"
            />
          </div>
          <div>
            <Label htmlFor="campaign-reviewer">Who reviews</Label>
            <Select
              id="campaign-reviewer"
              value={form.reviewer}
              onChange={(e) =>
                setForm({ ...form, reviewer: e.target.value as NewCampaign["reviewer"] })
              }
            >
              <option value="manager">Each person's manager</option>
              <option value="owner">The owner of each application or role</option>
            </Select>
          </div>
          <div>
            <Label htmlFor="campaign-due">Reviewers have</Label>
            <Select
              id="campaign-due"
              value={form.due_in_days}
              onChange={(e) => setForm({ ...form, due_in_days: Number(e.target.value) })}
            >
              {[7, 14, 30, 60, 90].map((d) => (
                <option key={d} value={d}>
                  {d} days
                </option>
              ))}
            </Select>
          </div>
          <div>
            <Label htmlFor="campaign-missing">If something is not reviewed in time</Label>
            <Select
              id="campaign-missing"
              value={form.on_missing}
              onChange={(e) =>
                setForm({ ...form, on_missing: e.target.value as NewCampaign["on_missing"] })
              }
            >
              <option value="keep">Keep it</option>
              <option value="revoke">Revoke it</option>
            </Select>
          </div>
          <fieldset className="sm:col-span-2">
            <legend className="mb-1 text-sm font-medium">What to review</legend>
            <p className="mb-2 text-xs text-slate-500">
              Pick applications, or none to review everything given by hand or by request.
              Birthright access is left out: review the rule instead.
            </p>
            {apps.isPending ? (
              <p className="text-sm text-slate-500">Loading applications…</p>
            ) : apps.error ? (
              <p className="text-sm text-red-700">{apps.error.message}</p>
            ) : (
              <div className="flex flex-wrap gap-x-4 gap-y-2">
                {apps.data.map((a) => (
                  <label key={a.id} className="flex items-center gap-2 text-sm">
                    <input
                      type="checkbox"
                      checked={form.application_ids.includes(a.id)}
                      onChange={() => toggleApp(a.id)}
                    />
                    {a.name}
                  </label>
                ))}
              </div>
            )}
          </fieldset>
          {launch.error && (
            <Alert variant="error" className="sm:col-span-2">
              {launch.error.message}
            </Alert>
          )}
          <div className="flex gap-2 sm:col-span-2">
            <Button type="submit" disabled={!form.name.trim() || launch.isPending}>
              {launch.isPending ? "Launching…" : "Launch campaign"}
            </Button>
            <Button type="button" variant="ghost" onClick={onDone}>
              Cancel
            </Button>
          </div>
        </form>
      </CardContent>
    </Card>
  );
}
