import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { Loaded } from "@/components/Loaded";
import { Alert, ErrorAlert } from "@/components/ui/alert";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Input, Label, Select } from "@/components/ui/field";
import { Manage } from "@/components/Manage";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import {
  createServiceToken,
  listCatalogApplications,
  listServiceTokens,
  listSources,
  revokeServiceToken,
  tokenScopes,
  type ServiceToken,
  type TokenScope,
  unpinnedScopes,
} from "@/lib/admin";
import { formatDate, formatTime } from "@/lib/utils";
import { useCanManage } from "@/lib/manage";

/** What a token may be pinned to: HR sources, and applications that read files. */
function useTargets() {
  const sources = useQuery({ queryKey: ["sources"], queryFn: listSources });
  const apps = useQuery({ queryKey: ["catalog-applications"], queryFn: listCatalogApplications });
  return {
    "identity_source.sync": (sources.data ?? []).map((s) => ({ id: s.id, name: s.name })),
    "application.import_accounts": (apps.data ?? [])
      .filter((a) => a.connector === "csv")
      .map((a) => ({ id: a.id, name: a.name })),
    "sign_in_event.send": [],
    "report.read": [],
  } satisfies Record<TokenScope, { id: string; name: string }[]>;
}

export function ServiceTokens() {
  const client = useQueryClient();
  const { data, error, isPending } = useQuery({
    queryKey: ["service-tokens"],
    queryFn: listServiceTokens,
  });
  const targets = useTargets();
  const [created, setCreated] = useState<{ token: ServiceToken; secret: string } | null>(null);
  const revoke = useMutation({
    mutationFn: revokeServiceToken,
    onSuccess: () => void client.invalidateQueries({ queryKey: ["service-tokens"] }),
  });
  const canManage = useCanManage();

  const describe = (scope: string) => {
    const [action, target] = scope.split(":") as [TokenScope, string | undefined];
    const what = tokenScopes[action] ?? action;
    if (unpinnedScopes.includes(action)) return what;
    if (!target) return `${what}: any`;
    return `${what}: ${targets[action]?.find((t) => t.id === target)?.name ?? target}`;
  };

  return (
    <div className="flex flex-col gap-4">
      <p className="text-sm text-slate-500">
        Scripts and scheduled jobs use a token to send HR exports, account files and sign-ins, or to
        download compliance reports, without anyone signing in. A token can only do what you allow,
        stops working on its expiry date, and everything it does is in the audit log under its name.
      </p>
      <Manage>
        <NewToken onCreated={setCreated} />
      </Manage>
      {created && <CreatedToken created={created} />}
      <Loaded loading={isPending} error={error} empty={data?.length === 0 && "No token yet."}>
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>Name</TableHead>
              <TableHead>May</TableHead>
              <TableHead>Expires</TableHead>
              <TableHead>Last used</TableHead>
              <TableHead />
            </TableRow>
          </TableHeader>
          <TableBody>
            {data?.map((t) => {
              const expired = new Date(t.expires_at) <= new Date();
              return (
                <TableRow key={t.id} className="align-top">
                  <TableCell>
                    <div className="font-medium">{t.name}</div>
                    <code className="text-xs text-slate-500">{t.prefix}…</code>
                    {t.creator_name && (
                      <div className="text-xs text-slate-500">Created by {t.creator_name}</div>
                    )}
                    {t.creator_left && !t.revoked_at && !expired && (
                      <Badge variant="amber" className="mt-1">
                        Creator left: check who looks after it
                      </Badge>
                    )}
                  </TableCell>
                  <TableCell className="text-sm">
                    <ul>
                      {t.scopes.map((s) => (
                        <li key={s}>{describe(s)}</li>
                      ))}
                    </ul>
                  </TableCell>
                  <TableCell className="text-sm whitespace-nowrap">
                    {formatDate(t.expires_at)}
                  </TableCell>
                  <TableCell className="text-sm text-slate-500">
                    {t.last_used_at ? formatTime(t.last_used_at) : "Never"}
                  </TableCell>
                  <TableCell className="text-right">
                    {t.revoked_at || expired ? (
                      <Badge>{t.revoked_at ? "Revoked" : "Expired"}</Badge>
                    ) : (
                      canManage && (
                        <Button
                          variant="outline"
                          size="sm"
                          disabled={revoke.isPending}
                          onClick={() => {
                            if (
                              window.confirm(`Revoke ${t.name}? Jobs using it get 401 right away.`)
                            )
                              revoke.mutate(t.id);
                          }}
                        >
                          Revoke
                        </Button>
                      )
                    )}
                  </TableCell>
                </TableRow>
              );
            })}
          </TableBody>
        </Table>
      </Loaded>
      <ErrorAlert error={revoke.error} />
    </div>
  );
}

type Grant = { action: TokenScope; target: string };

function NewToken({
  onCreated,
}: {
  onCreated: (c: { token: ServiceToken; secret: string }) => void;
}) {
  const client = useQueryClient();
  const targets = useTargets();
  const [name, setName] = useState("");
  const [grants, setGrants] = useState<Grant[]>([{ action: "identity_source.sync", target: "" }]);
  const [days, setDays] = useState(365);
  const create = useMutation({
    mutationFn: () =>
      createServiceToken({
        name,
        scopes: grants.map((g) => (g.target ? `${g.action}:${g.target}` : g.action)),
        expires_in_days: days,
      }),
    onSuccess: (r) => {
      onCreated(r);
      setName("");
      setGrants([{ action: "identity_source.sync", target: "" }]);
      void client.invalidateQueries({ queryKey: ["service-tokens"] });
    },
  });
  const change = (i: number, g: Grant) => setGrants(grants.map((old, n) => (n === i ? g : old)));
  return (
    <Card>
      <CardHeader>
        <CardTitle>New token</CardTitle>
      </CardHeader>
      <CardContent>
        <form
          aria-label="New token"
          className="flex flex-col gap-4"
          onSubmit={(e) => {
            e.preventDefault();
            create.mutate();
          }}
        >
          <div className="grid gap-4 sm:grid-cols-2">
            <div>
              <Label htmlFor="token-name">Name</Label>
              <Input
                id="token-name"
                required
                value={name}
                onChange={(e) => setName(e.target.value)}
                placeholder="Nightly HR export"
              />
            </div>
            <div>
              <Label htmlFor="token-days">Expires after</Label>
              <Select
                id="token-days"
                value={days}
                onChange={(e) => setDays(Number(e.target.value))}
              >
                <option value={30}>30 days</option>
                <option value={90}>90 days</option>
                <option value={180}>180 days</option>
                <option value={365}>1 year</option>
              </Select>
            </div>
          </div>
          <fieldset className="flex flex-col gap-2">
            <legend className="mb-1 text-sm font-medium">What it may do</legend>
            {grants.map((g, i) => (
              <div key={i} className="flex flex-wrap items-center gap-2">
                <Select
                  aria-label={`Permission ${i + 1}`}
                  className="w-auto"
                  value={g.action}
                  onChange={(e) => change(i, { action: e.target.value as TokenScope, target: "" })}
                >
                  {Object.entries(tokenScopes).map(([k, label]) => (
                    <option key={k} value={k}>
                      {label}
                    </option>
                  ))}
                </Select>
                {!unpinnedScopes.includes(g.action) && (
                  <Select
                    aria-label={`Permission ${i + 1} applies to`}
                    className="w-auto"
                    value={g.target}
                    onChange={(e) => change(i, { ...g, target: e.target.value })}
                  >
                    <option value="">Any</option>
                    {targets[g.action].map((t) => (
                      <option key={t.id} value={t.id}>
                        {t.name}
                      </option>
                    ))}
                  </Select>
                )}
                {grants.length > 1 && (
                  <Button
                    type="button"
                    variant="ghost"
                    size="sm"
                    onClick={() => setGrants(grants.filter((_, n) => n !== i))}
                  >
                    Remove
                  </Button>
                )}
              </div>
            ))}
            <div>
              <Button
                type="button"
                variant="ghost"
                size="sm"
                onClick={() =>
                  setGrants([...grants, { action: "identity_source.sync", target: "" }])
                }
              >
                Add another permission
              </Button>
            </div>
          </fieldset>
          <ErrorAlert error={create.error} />
          <div>
            <Button type="submit" disabled={create.isPending}>
              Create token
            </Button>
          </div>
        </form>
      </CardContent>
    </Card>
  );
}

/** The secret, shown once, with how to use it. */
function CreatedToken({ created }: { created: { token: ServiceToken; secret: string } }) {
  const [first] = created.token.scopes;
  const [action, target = "<id>"] = (first ?? "").split(":");
  const path =
    action === "application.import_accounts"
      ? `/api/v1/applications/${target}/account-imports`
      : `/api/v1/identity-sources/${target}/runs`;
  return (
    <Alert variant="success">
      <div>
        New token {created.token.name}, shown only now:{" "}
        <code className="break-all">{created.secret}</code>
      </div>
      <div className="mt-2">Keep it with the job that uses it, for example:</div>
      <pre className="mt-1 overflow-x-auto rounded bg-white/70 p-2 text-xs">
        {`curl -X POST -H "Authorization: Bearer $GRANTRA_TOKEN" \\
  -H "Content-Type: text/csv" --data-binary @export.csv \\
  "${window.location.origin}${path}?dry_run=true"`}
      </pre>
    </Alert>
  );
}
