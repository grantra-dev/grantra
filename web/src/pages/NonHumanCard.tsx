import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { PersonSearch } from "@/components/PersonSearch";
import { Alert } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Input, Label, Textarea } from "@/components/ui/field";
import {
  getNonHuman,
  retireNonHuman,
  updateNonHuman,
  type NonHuman,
  type NonHumanInput,
} from "@/lib/nhi";
import { formatDate } from "@/lib/utils";
import { Flags } from "./NonHuman";

/** What needs a look about a service account, agent or workload, and, for admins, changing it. */
export function NonHumanCard({ id, canChange }: { id: string; canChange: boolean }) {
  const { data: n, error } = useQuery({
    queryKey: ["non-human", id],
    queryFn: () => getNonHuman(id),
  });
  const [editing, setEditing] = useState(false);
  return (
    <Card>
      <CardHeader className="flex flex-row flex-wrap items-start justify-between gap-3">
        <CardTitle>Governance</CardTitle>
        {canChange && n && n.status !== "terminated" && (
          <Button size="sm" variant="outline" onClick={() => setEditing(!editing)}>
            {editing ? "Cancel" : "Change"}
          </Button>
        )}
      </CardHeader>
      <CardContent className="flex flex-col gap-3 text-sm">
        {error && <Alert variant="error">{error.message}</Alert>}
        {n && (
          <>
            {n.flags.length > 0 ? (
              <div className="flex flex-wrap gap-1.5">
                <Flags flags={n.flags} />
              </div>
            ) : (
              <p className="text-slate-600">Nothing needs a look.</p>
            )}
            <p className="text-slate-600">
              {n.accounts} {n.accounts === 1 ? "account" : "accounts"}, {n.vaulted} with the secret
              in the vault; {n.risky_held} high or critical risk{" "}
              {n.risky_held === 1 ? "entitlement" : "entitlements"}. Last sign-in:{" "}
              {n.last_used_at ? formatDate(n.last_used_at) : "unknown"}.
            </p>
            {editing && <Edit n={n} onDone={() => setEditing(false)} />}
          </>
        )}
      </CardContent>
    </Card>
  );
}

function Edit({ n, onDone }: { n: NonHuman; onDone: () => void }) {
  const client = useQueryClient();
  const [owner, setOwner] = useState<{ id: string; name: string }>();
  const [purpose, setPurpose] = useState(n.purpose ?? "");
  const [end, setEnd] = useState(n.end_date ?? "");
  const [spiffe, setSpiffe] = useState(n.spiffe_id ?? "");
  const [reason, setReason] = useState("");
  const refresh = async () => {
    await Promise.all(
      [["non-human"], ["identity", n.id]].map((queryKey) => client.invalidateQueries({ queryKey })),
    );
    onDone();
  };
  const save = useMutation({
    mutationFn: () => {
      const input: NonHumanInput = { purpose: purpose.trim(), end_date: end };
      if (owner) input.owner_id = owner.id;
      if (n.kind === "workload" || n.spiffe_id) input.spiffe_id = spiffe.trim();
      return updateNonHuman(n.id, input);
    },
    onSuccess: refresh,
  });
  const retire = useMutation({
    mutationFn: () => retireNonHuman(n.id, reason.trim()),
    onSuccess: refresh,
  });
  return (
    <div className="flex flex-col gap-4 border-t border-slate-100 pt-3">
      <form
        className="flex flex-col gap-3"
        onSubmit={(e) => {
          e.preventDefault();
          save.mutate();
        }}
      >
        <div className="flex flex-col gap-2">
          <Label htmlFor="nhi-owner">Owner</Label>
          {owner ? (
            <p>
              <span className="font-medium">{owner.name}</span>{" "}
              <button
                type="button"
                className="text-brand-700 hover:underline"
                onClick={() => setOwner(undefined)}
              >
                Keep {n.owner_name || "nobody"}
              </button>
            </p>
          ) : (
            <PersonSearch
              id="nhi-owner"
              placeholder={`Now ${n.owner_name || "nobody"}: search someone else`}
              action="Hand it to"
              skip={n.owner_id ? [n.owner_id] : []}
              onPick={(p) => setOwner({ id: p.id, name: p.display_name })}
            />
          )}
        </div>
        <div>
          <Label htmlFor="nhi-purpose">What it is for</Label>
          <Textarea
            id="nhi-purpose"
            value={purpose}
            maxLength={500}
            onChange={(e) => setPurpose(e.target.value)}
          />
        </div>
        <div>
          <Label htmlFor="nhi-end">Ends on</Label>
          <Input id="nhi-end" type="date" value={end} onChange={(e) => setEnd(e.target.value)} />
        </div>
        {(n.kind === "workload" || n.spiffe_id) && (
          <div>
            <Label htmlFor="nhi-spiffe">SPIFFE ID</Label>
            <Input id="nhi-spiffe" value={spiffe} onChange={(e) => setSpiffe(e.target.value)} />
          </div>
        )}
        {save.error && <Alert variant="error">{save.error.message}</Alert>}
        <div>
          <Button type="submit" size="sm" disabled={save.isPending}>
            Save
          </Button>
        </div>
      </form>
      <form
        className="flex flex-col gap-2 border-t border-slate-100 pt-3"
        onSubmit={(e) => {
          e.preventDefault();
          retire.mutate();
        }}
      >
        <Label htmlFor="nhi-retire">Retire it</Label>
        <p className="text-xs text-slate-500">
          Grantra removes the access it gave it and, where provisioning is on, disables its
          accounts. This cannot be undone.
        </p>
        <Input
          id="nhi-retire"
          value={reason}
          onChange={(e) => setReason(e.target.value)}
          placeholder="Why, such as: replaced by billing-sync-v2"
          required
        />
        {retire.error && <Alert variant="error">{retire.error.message}</Alert>}
        <div>
          <Button type="submit" size="sm" variant="destructive" disabled={retire.isPending}>
            Retire {n.name}
          </Button>
        </div>
      </form>
    </div>
  );
}
