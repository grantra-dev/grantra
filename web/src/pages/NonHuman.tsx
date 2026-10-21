import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { FormActions } from "@/components/FormActions";
import { useState } from "react";
import { Link, useSearchParams } from "react-router";
import { FilterBox } from "@/components/FilterBox";
import { Loaded } from "@/components/Loaded";
import { Manage } from "@/components/Manage";
import { PickPerson, type Picked } from "@/components/PersonSearch";
import { ShowMore } from "@/components/ShowMore";
import { Tabs } from "@/components/Tabs";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Input, Label, Select, Textarea } from "@/components/ui/field";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { usePages } from "@/lib/hooks";
import {
  createNonHuman,
  flagInfo,
  flags,
  getNonHumanSummary,
  identityKindLabels,
  listCandidates,
  listNonHuman,
  nonHumanKinds,
  nonHumanPageSize,
  type Candidate,
  type Flag,
  type NonHuman,
  type NonHumanFilter,
  type NonHumanKind,
} from "@/lib/nhi";
import { formatDate } from "@/lib/utils";

type Tab = "identities" | "unowned";

/** Governance → Service accounts and agents: programs with access, and who answers for each. */
export function NonHumanIdentities() {
  const [params, setParams] = useSearchParams();
  const tab: Tab = params.get("tab") === "unowned" ? "unowned" : "identities";
  return (
    <div>
      <Tabs
        tabs={[
          ["identities", "Service accounts and agents"],
          ["unowned", "Accounts nobody owns"],
        ]}
        current={tab}
        onSelect={(t) => setParams(t === "identities" ? {} : { tab: t })}
      />
      {tab === "identities" ? <Registry /> : <Unowned />}
    </div>
  );
}

function Registry() {
  const [filter, setFilter] = useState<NonHumanFilter>({});
  const [adding, setAdding] = useState(false);
  const counts = useQuery({ queryKey: ["non-human", "summary"], queryFn: getNonHumanSummary });
  const list = usePages(
    ["non-human", "list", filter],
    (after?: string) => listNonHuman(filter, after),
    nonHumanPageSize,
  );
  const filtered = !!(filter.kind || filter.flag || filter.q);
  return (
    <div className="flex flex-col gap-4">
      <Card>
        <CardContent className="pt-5 text-sm text-slate-600">
          Service accounts, AI agents and workloads hold access like people do, but nobody joins or
          leaves with them. Each one here has an owner who answers for it: they review its access
          and approve what it asks for, and when they leave it passes to their manager. Its accounts
          in applications are linked to it, and workloads can ask for authorization with their
          SPIFFE ID.
        </CardContent>
      </Card>
      <div className="flex flex-wrap items-center gap-2" role="group" aria-label="Show">
        <Button
          size="sm"
          variant={filter.flag ? "outline" : "default"}
          onClick={() => setFilter({ ...filter, flag: undefined })}
        >
          All{counts.data && ` (${counts.data.total})`}
        </Button>
        {flags.map((f) => (
          <Button
            key={f}
            size="sm"
            variant={filter.flag === f ? "default" : "outline"}
            title={flagInfo[f].why}
            onClick={() => setFilter({ ...filter, flag: filter.flag === f ? undefined : f })}
          >
            {flagInfo[f].label}
            {counts.data && ` (${counts.data[f]})`}
          </Button>
        ))}
      </div>
      <div className="flex flex-wrap items-end gap-3">
        <div>
          <Label htmlFor="nhi-kind">Kind</Label>
          <Select
            id="nhi-kind"
            value={filter.kind ?? ""}
            onChange={(e) =>
              setFilter({ ...filter, kind: (e.target.value || undefined) as NonHumanKind })
            }
          >
            <option value="">All kinds</option>
            <KindOptions />
          </Select>
        </div>
        <FilterBox
          label="Find"
          placeholder="Name, purpose or SPIFFE ID"
          value={filter.q ?? ""}
          onChange={(q) => setFilter({ ...filter, q: q.trim() || undefined })}
        />
        <Manage>
          <Button className="ml-auto" onClick={() => setAdding(!adding)}>
            {adding ? "Cancel" : "Add one"}
          </Button>
        </Manage>
      </div>
      {adding && <NewNonHuman onDone={() => setAdding(false)} />}
      <Loaded
        loading={list.isPending}
        error={list.error}
        empty={
          list.rows.length === 0 &&
          (filtered
            ? "Nothing matches."
            : "None yet. Add the service accounts and agents your company runs, or start from the accounts nobody owns.")
        }
      >
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>Name</TableHead>
              <TableHead>Owner</TableHead>
              <TableHead>Accounts</TableHead>
              <TableHead>Last sign-in</TableHead>
              <TableHead>Ends</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {list.rows.map((n) => (
              <Row key={n.id} n={n} />
            ))}
          </TableBody>
        </Table>
        <ShowMore query={list} />
      </Loaded>
    </div>
  );
}

const KindOptions = () =>
  nonHumanKinds.map((k) => (
    <option key={k} value={k}>
      {identityKindLabels[k]}
    </option>
  ));

const flagVariant: Record<Flag, "red" | "amber"> = {
  no_owner: "red",
  unvaulted: "red",
  unused: "amber",
  expiring: "amber",
};

/** The flags a non-human identity has, as badges that say why on hover. */
export function Flags({ flags }: { flags: Flag[] }) {
  return flags.map((f) => (
    <Badge key={f} variant={flagVariant[f]} title={flagInfo[f].why}>
      {flagInfo[f].label}
    </Badge>
  ));
}

function Row({ n }: { n: NonHuman }) {
  return (
    <TableRow className="align-top">
      <TableCell>
        <Link to={`/people/${n.id}`} className="font-medium text-brand-700 hover:underline">
          {n.name}
        </Link>
        <div className="flex flex-wrap items-center gap-1.5 text-slate-500">
          {identityKindLabels[n.kind]}
          {n.risky_held > 0 && (
            <Badge variant="red" title={`${n.risky_held} high or critical risk entitlements`}>
              High-risk access
            </Badge>
          )}
          <Flags flags={n.flags} />
        </div>
        {n.purpose && <div className="text-slate-500">{n.purpose}</div>}
      </TableCell>
      <TableCell>
        {n.owner_id ? (
          <Link to={`/people/${n.owner_id}`} className="text-brand-700 hover:underline">
            {n.owner_name}
          </Link>
        ) : (
          <span className="text-red-700">Nobody</span>
        )}
      </TableCell>
      <TableCell className="whitespace-nowrap">
        {n.accounts}
        {n.accounts > 0 && <span className="text-slate-500"> · {n.vaulted} in the vault</span>}
      </TableCell>
      <TableCell className="whitespace-nowrap">
        {n.last_used_at ? formatDate(n.last_used_at) : "Unknown"}
      </TableCell>
      <TableCell className="whitespace-nowrap">
        {n.end_date ? formatDate(n.end_date) : "—"}
      </TableCell>
    </TableRow>
  );
}

/** A form for a new service account, agent or workload; with account, for that account's. */
function NewNonHuman({ account, onDone }: { account?: Candidate; onDone: () => void }) {
  const client = useQueryClient();
  const [kind, setKind] = useState<NonHumanKind>("service");
  const [name, setName] = useState(account?.username ?? "");
  const [purpose, setPurpose] = useState("");
  const [owner, setOwner] = useState<Picked>();
  const [spiffe, setSpiffe] = useState("");
  const [end, setEnd] = useState("");
  const key = account?.account_id ?? "new";
  const create = useMutation({
    mutationFn: () =>
      createNonHuman({
        kind,
        name: name.trim(),
        purpose: purpose.trim(),
        owner_id: owner?.id,
        spiffe_id: spiffe.trim(),
        end_date: end,
        account_ids: account ? [account.account_id] : [],
      }),
    onSuccess: async () => {
      await client.invalidateQueries({ queryKey: ["non-human"] });
      onDone();
    },
  });
  return (
    <Card>
      <CardHeader>
        <CardTitle>
          {account ? `Who answers for ${account.username}?` : "A service account or agent"}
        </CardTitle>
      </CardHeader>
      <CardContent>
        <form
          className="grid gap-4 sm:grid-cols-2"
          onSubmit={(e) => {
            e.preventDefault();
            create.mutate();
          }}
        >
          <div>
            <Label htmlFor={`nhi-new-kind-${key}`}>Kind</Label>
            <Select
              id={`nhi-new-kind-${key}`}
              value={kind}
              onChange={(e) => setKind(e.target.value as NonHumanKind)}
            >
              <KindOptions />
            </Select>
          </div>
          <div>
            <Label htmlFor={`nhi-new-name-${key}`}>Name</Label>
            <Input
              id={`nhi-new-name-${key}`}
              value={name}
              onChange={(e) => setName(e.target.value)}
              required
            />
          </div>
          <div className="flex flex-col gap-2">
            <Label htmlFor={`nhi-new-owner-${key}`}>Owner</Label>
            <PickPerson
              id={`nhi-new-owner-${key}`}
              placeholder="The person who answers for it"
              value={owner}
              onChange={setOwner}
            />
          </div>
          <div>
            <Label htmlFor={`nhi-new-end-${key}`}>Ends on (optional)</Label>
            <Input
              id={`nhi-new-end-${key}`}
              type="date"
              value={end}
              onChange={(e) => setEnd(e.target.value)}
            />
          </div>
          <div className="sm:col-span-2">
            <Label htmlFor={`nhi-new-purpose-${key}`}>What it is for</Label>
            <Textarea
              id={`nhi-new-purpose-${key}`}
              value={purpose}
              maxLength={500}
              onChange={(e) => setPurpose(e.target.value)}
            />
          </div>
          {kind === "workload" && (
            <div className="sm:col-span-2">
              <Label htmlFor={`nhi-new-spiffe-${key}`}>SPIFFE ID (optional)</Label>
              <Input
                id={`nhi-new-spiffe-${key}`}
                value={spiffe}
                onChange={(e) => setSpiffe(e.target.value)}
                placeholder="spiffe://example.org/ns/prod/sa/billing"
              />
            </div>
          )}
          <FormActions
            action={create}
            submit="Add it"
            disabled={!owner || !name.trim()}
            onCancel={onDone}
            className="sm:col-span-2"
          />
        </form>
      </CardContent>
    </Card>
  );
}

function Unowned() {
  const { data, error, isPending } = useQuery({
    queryKey: ["non-human", "candidates"],
    queryFn: listCandidates,
  });
  const [open, setOpen] = useState<string>();
  const accounts = data?.accounts ?? [];
  return (
    <div className="flex flex-col gap-4">
      <Card>
        <CardContent className="pt-5 text-sm text-slate-600">
          Active accounts in your applications that no person or program in Grantra owns. Many are
          service accounts: the ones whose name looks like a program&apos;s come first, then the
          ones holding high or critical risk access. Give each one an owner, or link it to its
          person from the application&apos;s page.
          {data && accounts.length >= data.limit && ` Showing the first ${data.limit}.`}
        </CardContent>
      </Card>
      <Loaded
        loading={isPending}
        error={error}
        empty={accounts.length === 0 && "Every account has an owner."}
      >
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>Account</TableHead>
              <TableHead>Access</TableHead>
              <TableHead>Last sign-in</TableHead>
              <TableHead />
            </TableRow>
          </TableHeader>
          <TableBody>
            {accounts.map((c) => (
              <TableRow key={c.account_id} className="align-top">
                <TableCell>
                  <span className="font-medium">{c.username}</span>
                  <div className="flex flex-wrap items-center gap-1.5 text-slate-500">
                    <Link
                      to={`/governance/applications/${c.application_id}`}
                      className="hover:underline"
                    >
                      {c.application}
                    </Link>
                    {c.looks_like_service && <Badge>Looks like a program</Badge>}
                  </div>
                  {open === c.account_id && (
                    <div className="mt-3">
                      <NewNonHuman account={c} onDone={() => setOpen(undefined)} />
                    </div>
                  )}
                </TableCell>
                <TableCell>
                  {c.entitlements} {c.entitlements === 1 ? "entitlement" : "entitlements"}{" "}
                  {c.risky && <Badge variant="red">High risk</Badge>}{" "}
                  {c.vaulted && <Badge variant="green">In the vault</Badge>}
                </TableCell>
                <TableCell className="whitespace-nowrap">
                  {c.last_login_at ? formatDate(c.last_login_at) : "Unknown"}
                </TableCell>
                <TableCell className="text-right">
                  <Manage>
                    {open !== c.account_id && (
                      <Button size="sm" variant="outline" onClick={() => setOpen(c.account_id)}>
                        Give it an owner
                      </Button>
                    )}
                  </Manage>
                </TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      </Loaded>
    </div>
  );
}
