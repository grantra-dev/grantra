import { useQuery } from "@tanstack/react-query";
import { useState } from "react";
import { useSearchParams } from "react-router";
import { Loaded } from "@/components/Loaded";
import { PathChain } from "@/components/PathChain";
import { RiskBadge } from "@/components/RiskBadge";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Input, Label } from "@/components/ui/field";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { getAttackPaths, listAttackTargets, type AttackTarget } from "@/lib/insights";
import { capitalize, hasWords } from "@/lib/utils";

/** Security → Attack paths: who reaches the riskiest access, and how; ?target=<id> opens one. */
export function AttackPaths() {
  const [params, setParams] = useSearchParams();
  const selected = params.get("target");
  const [text, setText] = useState("");
  const { data, error, isPending } = useQuery({
    queryKey: ["attack-paths"],
    queryFn: listAttackTargets,
  });
  const shown = (data ?? []).filter((t) => hasWords(text, t.name, t.application));
  return (
    <div className="flex flex-col gap-4">
      {selected && <TargetDetail id={selected} onClose={() => setParams({})} />}
      <Card>
        <CardContent className="pt-5 text-sm text-slate-600">
          Every high and critical risk entitlement someone can reach, counting every way there: an
          assignment, a role, a birthright rule, an eligibility, an account that holds it in the
          application, or the password of a shared account the vault or the gateway hands out. The
          last two are the ones people miss. Fewer people for good, and none through a shared
          account or outside Grantra, is what to aim for.
        </CardContent>
      </Card>
      <div className="max-w-sm">
        <Label htmlFor="paths-find">Find</Label>
        <Input
          id="paths-find"
          type="search"
          value={text}
          onChange={(e) => setText(e.target.value)}
          placeholder="Access or application"
        />
      </div>
      <Loaded
        loading={isPending}
        error={error}
        empty={
          shown.length === 0 &&
          (text ? "Nothing matches." : "Nobody reaches high or critical risk access.")
        }
      >
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>Access</TableHead>
              <TableHead className="text-right">People</TableHead>
              <TableHead className="text-right">For good</TableHead>
              <TableHead className="text-right">Through a shared account</TableHead>
              <TableHead className="text-right">Not given by Grantra</TableHead>
              <TableHead className="text-right">Accounts nobody owns</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {shown.map((t) => (
              <TableRow key={t.id}>
                <TableCell>
                  <button
                    type="button"
                    className="text-left font-medium text-brand-700 hover:underline"
                    onClick={() => setParams({ target: t.id })}
                  >
                    {t.name}
                  </button>
                  <div className="flex flex-wrap items-center gap-2 text-slate-500">
                    {t.application} <RiskBadge risk={t.risk} />
                    {t.jit_only && <Badge variant="green">Just in time only</Badge>}
                  </div>
                </TableCell>
                <TableCell className="text-right">{t.people}</TableCell>
                <Count n={t.standing} />
                <Count n={t.credential} />
                <Count n={t.outside} />
                <Count n={t.orphans} />
              </TableRow>
            ))}
          </TableBody>
        </Table>
      </Loaded>
    </div>
  );
}

/** A count that is worth a look when it is not zero. */
function Count({ n }: { n: number }) {
  return (
    <TableCell
      className={n > 0 ? "text-right font-medium text-red-700" : "text-right text-slate-400"}
    >
      {n}
    </TableCell>
  );
}

/** What to do about a target, from what its paths say. */
function advice(t: AttackTarget): string[] {
  const out: string[] = [];
  if (t.standing > 0 && !t.jit_only) {
    out.push(
      "Make it just-in-time only (Governance → Standing access), so people turn it on for a few hours instead of holding it.",
    );
  }
  if (t.credential > 0) {
    out.push(
      "Whoever may use the shared account below reaches this too. Take the access off the shared account, or treat its vault access as this risky.",
    );
  }
  if (t.outside > 0) {
    out.push(
      "Some accounts hold it in the application without Grantra giving it: give it through Grantra, or take it away there.",
    );
  }
  if (t.orphans > 0) {
    out.push(
      "Accounts nobody owns hold it: link them to their owner, put them in the vault, or disable them.",
    );
  }
  return out;
}

function TargetDetail({ id, onClose }: { id: string; onClose: () => void }) {
  const { data, error, isPending } = useQuery({
    queryKey: ["attack-paths", id],
    queryFn: () => getAttackPaths(id),
  });
  return (
    <Card aria-label="Attack paths">
      {isPending ? (
        <p className="p-6 text-sm text-slate-500">Loading…</p>
      ) : error ? (
        <p role="alert" className="p-6 text-sm text-red-700">
          Could not load these paths: {error.message}
        </p>
      ) : (
        <>
          <CardHeader className="flex flex-row flex-wrap items-start justify-between gap-3">
            <div>
              <CardTitle className="flex flex-wrap items-center gap-2">
                {data.name} <RiskBadge risk={data.risk} />
              </CardTitle>
              <p className="mt-1 text-sm text-slate-600">
                {data.application} · {data.people} {data.people === 1 ? "person" : "people"} can
                reach it
              </p>
            </div>
            <Button variant="ghost" size="sm" onClick={onClose}>
              Close
            </Button>
          </CardHeader>
          <CardContent className="flex flex-col gap-5">
            {advice(data).length > 0 && (
              <ul className="list-disc pl-5 text-sm text-slate-700">
                {advice(data).map((a) => (
                  <li key={a}>{a}</li>
                ))}
              </ul>
            )}
            <ul className="flex flex-col divide-y divide-slate-100">
              {data.paths.map((p, i) => (
                <li key={i} className="py-2">
                  <PathChain path={p} />
                </li>
              ))}
            </ul>
            {data.truncated && (
              <p className="text-sm text-slate-500">Showing the first {data.paths.length} ways.</p>
            )}
            {data.orphan_accounts.length > 0 && (
              <section>
                <h3 className="mb-2 text-sm font-semibold">Accounts nobody owns</h3>
                <ul className="flex flex-col gap-1 text-sm">
                  {data.orphan_accounts.map((o) => (
                    <li key={o.account_id}>
                      <span className="font-medium">{o.account}</span>{" "}
                      <span className="text-slate-500">
                        in {o.application}
                        {o.owner &&
                          ` · belonged to ${o.owner} (${capitalize(o.owner_status ?? "")})`}
                      </span>
                    </li>
                  ))}
                </ul>
              </section>
            )}
          </CardContent>
        </>
      )}
    </Card>
  );
}
