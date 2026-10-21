import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { Loaded } from "@/components/Loaded";
import { Alert, ErrorAlert } from "@/components/ui/alert";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Manage } from "@/components/Manage";
import { Input, Label, Select, Textarea } from "@/components/ui/field";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import {
  createAPIKey,
  getAppAuthz,
  revokeAPIKey,
  setAppPolicies,
  testAuthz,
  type APIKey,
  type Question,
} from "@/lib/appauthz";
import { formatTime } from "@/lib/utils";
import { useCanManage } from "@/lib/manage";

const example = `// Who may do what in this app. principal is the person, with what Grantra
// knows: entitlements (in this app), roles, department, title, manager...
@id("finance approves invoices")
permit (principal, action == Action::"approve", resource is Invoice)
when {
  principal.entitlements.contains("approver") &&
  principal.department == "Finance" &&
  resource.amount < 10000
};

@id("nobody approves their own invoice")
forbid (principal, action == Action::"approve", resource is Invoice)
when { resource has submitter && resource.submitter == principal.email };
`;

export function Authorization({ appId }: { appId: string }) {
  const { data, error, isPending } = useQuery({
    queryKey: ["app-authz", appId],
    queryFn: () => getAppAuthz(appId),
  });
  const [draft, setDraft] = useState<string | null>(null);
  const policies = draft ?? data?.policies ?? "";
  return (
    <Loaded loading={isPending} error={error}>
      {data && (
        <div className="flex flex-col gap-4">
          <p className="text-sm text-slate-600">
            Your app&apos;s backend asks Grantra whether someone may do something, with{" "}
            <code>POST /api/v1/authz/check</code> and an API key. Grantra answers with these Cedar
            policies, filling in the person from what it knows. People who are not active are always
            denied.
          </p>
          <Policies
            appId={appId}
            stored={data.policies}
            value={policies}
            onChange={setDraft}
            updated={data.updated_at}
          />
          <Playground appId={appId} draft={draft ?? undefined} />
          <Keys appId={appId} keys={data.keys} />
        </div>
      )}
    </Loaded>
  );
}

function Policies({
  appId,
  stored,
  value,
  onChange,
  updated,
}: {
  appId: string;
  stored: string;
  value: string;
  onChange: (v: string | null) => void;
  updated?: string;
}) {
  const client = useQueryClient();
  const save = useMutation({
    mutationFn: () => setAppPolicies(appId, value),
    onSuccess: () => {
      onChange(null);
      void client.invalidateQueries({ queryKey: ["app-authz", appId] });
    },
  });
  const changed = value !== stored;
  const canManage = useCanManage();
  return (
    <Card>
      <CardHeader>
        <CardTitle>Policies</CardTitle>
        <CardDescription>
          Cedar policies. With none, every question is denied.
          {updated && ` Last saved ${formatTime(updated)}.`}
        </CardDescription>
      </CardHeader>
      <CardContent className="flex flex-col gap-3">
        <Textarea
          aria-label="Cedar policies"
          rows={14}
          spellCheck={false}
          className="font-mono text-xs"
          value={value}
          placeholder={example}
          readOnly={!canManage}
          onChange={(e) => onChange(e.target.value)}
        />
        <Manage>
          <div className="flex flex-wrap items-center gap-2">
            <Button disabled={!changed || save.isPending} onClick={() => save.mutate()}>
              {save.isPending ? "Saving…" : "Save policies"}
            </Button>
            {changed && (
              <Button variant="ghost" onClick={() => onChange(null)}>
                Discard changes
              </Button>
            )}
            {!value && (
              <Button variant="outline" onClick={() => onChange(example)}>
                Start from an example
              </Button>
            )}
            {changed && (
              <span className="text-sm text-slate-500">
                Unsaved: the playground uses this draft.
              </span>
            )}
          </div>
        </Manage>
        <ErrorAlert error={save.error} />
      </CardContent>
    </Card>
  );
}

const principalKinds = [
  ["email", "Email"],
  ["username", "Username in this app"],
  ["external_id", "Employee ID"],
  ["id", "Grantra ID"],
  ["spiffe_id", "SPIFFE ID (workloads)"],
] as const;

function Playground({ appId, draft }: { appId: string; draft?: string }) {
  const [by, setBy] = useState<(typeof principalKinds)[number][0]>("email");
  const [who, setWho] = useState("");
  const [action, setAction] = useState("");
  const [type, setType] = useState("");
  const [resourceId, setResourceId] = useState("");
  const [attributes, setAttributes] = useState("");
  const [context, setContext] = useState("");
  const [bad, setBad] = useState("");
  const run = useMutation({
    mutationFn: (q: Question) => testAuthz(appId, q, draft),
  });
  const json = (label: string, s: string) => {
    if (!s.trim()) return undefined;
    try {
      return JSON.parse(s) as unknown;
    } catch {
      throw new Error(`${label} must be a JSON object`);
    }
  };
  const answer = run.data;
  return (
    <Card>
      <CardHeader>
        <CardTitle>Try it</CardTitle>
        <CardDescription>
          Ask a question as your app would, and see the answer and what the policies see of the
          person. Nothing is saved.
        </CardDescription>
      </CardHeader>
      <CardContent className="flex flex-col gap-3">
        <form
          className="grid gap-3 sm:grid-cols-2"
          onSubmit={(e) => {
            e.preventDefault();
            try {
              setBad("");
              run.mutate({
                principal: { [by]: who },
                action,
                resource: {
                  type,
                  id: resourceId,
                  attributes: json("Resource attributes", attributes),
                },
                context: json("Context", context),
              });
            } catch (err) {
              setBad((err as Error).message);
            }
          }}
        >
          <div className="flex gap-2">
            <div>
              <Label htmlFor="try-by">Person by</Label>
              <Select id="try-by" value={by} onChange={(e) => setBy(e.target.value as typeof by)}>
                {principalKinds.map(([k, l]) => (
                  <option key={k} value={k}>
                    {l}
                  </option>
                ))}
              </Select>
            </div>
            <div className="grow">
              <Label htmlFor="try-who">Person</Label>
              <Input
                id="try-who"
                required
                value={who}
                onChange={(e) => setWho(e.target.value)}
                placeholder="ana@example.com"
              />
            </div>
          </div>
          <div>
            <Label htmlFor="try-action">Action</Label>
            <Input
              id="try-action"
              required
              value={action}
              onChange={(e) => setAction(e.target.value)}
              placeholder="approve"
            />
          </div>
          <div>
            <Label htmlFor="try-type">Resource type</Label>
            <Input
              id="try-type"
              required
              value={type}
              onChange={(e) => setType(e.target.value)}
              placeholder="Invoice"
            />
          </div>
          <div>
            <Label htmlFor="try-id">Resource ID</Label>
            <Input
              id="try-id"
              required
              value={resourceId}
              onChange={(e) => setResourceId(e.target.value)}
              placeholder="42"
            />
          </div>
          <div>
            <Label htmlFor="try-attrs">Resource attributes (JSON)</Label>
            <Textarea
              id="try-attrs"
              rows={2}
              className="font-mono text-xs"
              value={attributes}
              onChange={(e) => setAttributes(e.target.value)}
              placeholder='{"amount": 1200, "submitter": "luis@example.com"}'
            />
          </div>
          <div>
            <Label htmlFor="try-context">Context (JSON)</Label>
            <Textarea
              id="try-context"
              rows={2}
              className="font-mono text-xs"
              value={context}
              onChange={(e) => setContext(e.target.value)}
              placeholder='{"mfa": true}'
            />
          </div>
          <div className="sm:col-span-2">
            <Button type="submit" disabled={run.isPending}>
              Ask
            </Button>
          </div>
        </form>
        {bad && <Alert variant="error">{bad}</Alert>}
        <ErrorAlert error={run.error} />
        {answer && (
          <div className="flex flex-col gap-2 text-sm">
            <Alert variant={answer.allowed ? "success" : "warning"}>
              {answer.allowed ? "Allowed" : "Denied"}
              {answer.policies.length > 0 && ` by ${answer.policies.join(", ")}`}
              {answer.reason === "unknown_person" && ": Grantra does not know this person."}
              {answer.reason === "person_not_active" && ": this person is not active."}
              {!answer.allowed &&
                !answer.reason &&
                answer.policies.length === 0 &&
                ": no policy permits it."}
            </Alert>
            {answer.errors?.map((e) => (
              <Alert key={e} variant="error">
                {e}
              </Alert>
            ))}
            {answer.person && (
              <div>
                <p className="font-medium">What the policies see as principal</p>
                <pre className="mt-1 overflow-x-auto rounded-md bg-slate-50 p-3 text-xs">
                  {JSON.stringify(
                    {
                      email: answer.person.email,
                      display_name: answer.person.display_name,
                      status: answer.person.status,
                      ...answer.person.attributes,
                      manager: answer.person.manager_email
                        ? { email: answer.person.manager_email }
                        : undefined,
                      roles: answer.person.roles,
                      entitlements: answer.person.entitlements,
                    },
                    null,
                    2,
                  )}
                </pre>
              </div>
            )}
          </div>
        )}
      </CardContent>
    </Card>
  );
}

function Keys({ appId, keys }: { appId: string; keys: APIKey[] }) {
  const client = useQueryClient();
  const [name, setName] = useState("");
  const [secret, setSecret] = useState<string | null>(null);
  const refresh = () => void client.invalidateQueries({ queryKey: ["app-authz", appId] });
  const create = useMutation({
    mutationFn: () => createAPIKey(appId, name),
    onSuccess: (r) => {
      setSecret(r.secret);
      setName("");
      refresh();
    },
  });
  const revoke = useMutation({
    mutationFn: (id: string) => revokeAPIKey(appId, id),
    onSuccess: refresh,
  });
  const canManage = useCanManage();
  return (
    <Card>
      <CardHeader>
        <CardTitle>API keys</CardTitle>
        <CardDescription>
          Your app&apos;s backend sends one as <code>Authorization: Bearer …</code>. A key only gets
          answers from this application&apos;s policies. Keep keys on servers, never in a browser or
          mobile app.
        </CardDescription>
      </CardHeader>
      <CardContent className="flex flex-col gap-3">
        {secret && (
          <Alert variant="success">
            New key, shown only now: <code className="break-all">{secret}</code>
          </Alert>
        )}
        {keys.length > 0 && (
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>Name</TableHead>
                <TableHead>Key</TableHead>
                <TableHead>Last used</TableHead>
                <TableHead />
              </TableRow>
            </TableHeader>
            <TableBody>
              {keys.map((k) => (
                <TableRow key={k.id}>
                  <TableCell className="font-medium">{k.name}</TableCell>
                  <TableCell>
                    <code>{k.prefix}…</code>
                  </TableCell>
                  <TableCell className="text-sm text-slate-500">
                    {k.last_used_at ? formatTime(k.last_used_at) : "Never"}
                  </TableCell>
                  <TableCell className="text-right">
                    {k.revoked_at ? (
                      <Badge>Revoked</Badge>
                    ) : (
                      canManage && (
                        <Button
                          variant="outline"
                          size="sm"
                          disabled={revoke.isPending}
                          onClick={() => {
                            if (
                              window.confirm(`Revoke ${k.name}? Apps using it get 401 right away.`)
                            )
                              revoke.mutate(k.id);
                          }}
                        >
                          Revoke
                        </Button>
                      )
                    )}
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        )}
        <Manage>
          <form
            className="flex flex-wrap items-end gap-2"
            onSubmit={(e) => {
              e.preventDefault();
              create.mutate();
            }}
          >
            <div>
              <Label htmlFor="key-name">Key name</Label>
              <Input
                id="key-name"
                required
                value={name}
                onChange={(e) => setName(e.target.value)}
                placeholder="billing backend (production)"
              />
            </div>
            <Button type="submit" disabled={create.isPending}>
              Create key
            </Button>
          </form>
        </Manage>
        <ErrorAlert error={create.error ?? revoke.error} />
      </CardContent>
    </Card>
  );
}
