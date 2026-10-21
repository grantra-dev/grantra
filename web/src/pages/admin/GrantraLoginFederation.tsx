import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { Manage } from "@/components/Manage";
import { Alert, ErrorAlert } from "@/components/ui/alert";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Input, Label, Select } from "@/components/ui/field";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import {
  createIdentityProvider,
  deleteIdentityProvider,
  importToLogin,
  setProviderDomains,
  type IdentityProvider,
  type ImportPlan,
  type ImportSet,
  type ProviderKind,
} from "@/lib/login";
import { useCanManage } from "@/lib/manage";

const kinds: { value: ProviderKind; label: string }[] = [
  { value: "entra", label: "Microsoft Entra ID" },
  { value: "okta", label: "Okta" },
  { value: "oidc", label: "OpenID Connect" },
  { value: "saml", label: "SAML" },
];

const kindLabel = (k: string) => kinds.find((x) => x.value === k)?.label ?? k;

const splitDomains = (s: string) =>
  s
    .split(/[\s,]+/)
    .map((d) => d.trim())
    .filter(Boolean);

export function IdentityProviders({ providers }: { providers: IdentityProvider[] }) {
  return (
    <Card>
      <CardHeader>
        <CardTitle>Identity providers</CardTitle>
        <CardDescription>
          Let people sign in with the provider your company already has. People whose email is in a
          provider's domains go straight to it; everyone else signs in with their Grantra Login
          password, so you can move over one domain at a time.
        </CardDescription>
      </CardHeader>
      <CardContent className="flex flex-col gap-4">
        {providers.length === 0 ? (
          <p className="text-sm text-slate-500">No identity providers yet.</p>
        ) : (
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>Provider</TableHead>
                <TableHead>Register at the provider</TableHead>
                <TableHead>Email domains sent to it</TableHead>
                <TableHead />
              </TableRow>
            </TableHeader>
            <TableBody>
              {providers.map((p) => (
                <Provider key={p.alias} provider={p} />
              ))}
            </TableBody>
          </Table>
        )}
        <Manage>
          <NewProvider />
        </Manage>
      </CardContent>
    </Card>
  );
}

function Provider({ provider: p }: { provider: IdentityProvider }) {
  const client = useQueryClient();
  const [domains, setDomains] = useState(p.domains.join(", "));
  const refresh = () => void client.invalidateQueries({ queryKey: ["login"] });
  const route = useMutation({
    mutationFn: () => setProviderDomains(p.alias, splitDomains(domains)),
    onSuccess: refresh,
  });
  const remove = useMutation({
    mutationFn: () => deleteIdentityProvider(p.alias),
    onSuccess: refresh,
  });
  const changed = splitDomains(domains).join(",") !== p.domains.join(",");
  const canManage = useCanManage();
  return (
    <TableRow>
      <TableCell>
        <span className="font-medium">{p.display_name || p.alias}</span>{" "}
        <Badge>{kindLabel(p.kind)}</Badge> {!p.enabled && <Badge>Disabled</Badge>}
        <div className="text-sm text-slate-500">
          {p.link_by_email ? "Links to existing accounts by email" : "Asks people to confirm"}
        </div>
      </TableCell>
      <TableCell className="text-sm">
        <div className="break-all">
          {p.kind === "saml" ? "Assertion consumer service: " : "Redirect URI: "}
          <code>{p.redirect_uri}</code>
        </div>
        {p.saml_metadata && (
          <a href={p.saml_metadata} className="text-brand-700 hover:underline">
            Service provider metadata
          </a>
        )}
      </TableCell>
      <TableCell>
        {!canManage && <span className="text-sm">{p.domains.join(", ") || "None"}</span>}
        <Manage>
          <form
            className="flex gap-2"
            onSubmit={(e) => {
              e.preventDefault();
              route.mutate();
            }}
          >
            <Input
              aria-label={`Email domains for ${p.display_name || p.alias}`}
              value={domains}
              onChange={(e) => setDomains(e.target.value)}
              placeholder="acme.com, acme.io"
            />
            <Button
              type="submit"
              variant="outline"
              size="sm"
              disabled={!changed || route.isPending}
            >
              Save
            </Button>
          </form>
        </Manage>
        <ErrorAlert error={route.error} className="mt-2" />
      </TableCell>
      <TableCell className="text-right">
        <Manage>
          <Button
            variant="outline"
            size="sm"
            disabled={remove.isPending}
            onClick={() => {
              if (
                window.confirm(
                  `Remove ${p.display_name || p.alias}? People who signed in with it keep their accounts, and need a Grantra Login password to sign in.`,
                )
              )
                remove.mutate();
            }}
          >
            Remove
          </Button>
        </Manage>
        <ErrorAlert error={remove.error} className="mt-2" />
      </TableCell>
    </TableRow>
  );
}

function NewProvider() {
  const client = useQueryClient();
  const empty = {
    kind: "entra" as ProviderKind,
    display_name: "",
    alias: "",
    tenant: "",
    domain: "",
    issuer: "",
    client_id: "",
    client_secret: "",
    metadata_url: "",
    email_attribute: "",
    link_by_email: true,
  };
  const [p, setP] = useState(empty);
  const field = (k: keyof typeof empty) => ({
    id: `idp-${k}`,
    value: p[k] as string,
    onChange: (e: { target: { value: string } }) => setP({ ...p, [k]: e.target.value }),
  });
  const create = useMutation({
    mutationFn: () => createIdentityProvider(p),
    onSuccess: () => {
      setP(empty);
      void client.invalidateQueries({ queryKey: ["login"] });
    },
  });
  const oidc = p.kind !== "saml";
  return (
    <form
      className="grid gap-3 border-t border-slate-200 pt-4 sm:grid-cols-2"
      onSubmit={(e) => {
        e.preventDefault();
        create.mutate();
      }}
    >
      <h4 className="text-sm font-medium sm:col-span-2">Add an identity provider</h4>
      <div>
        <Label htmlFor="idp-kind">Kind</Label>
        <Select
          id="idp-kind"
          value={p.kind}
          onChange={(e) => setP({ ...p, kind: e.target.value as ProviderKind })}
        >
          {kinds.map((k) => (
            <option key={k.value} value={k.value}>
              {k.label}
            </option>
          ))}
        </Select>
      </div>
      <div>
        <Label htmlFor="idp-display_name">Button label</Label>
        <Input required placeholder="Microsoft" {...field("display_name")} />
      </div>
      <div>
        <Label htmlFor="idp-alias">Alias</Label>
        <Input required placeholder="entra" pattern="[a-z0-9][a-z0-9-]*" {...field("alias")} />
      </div>
      {p.kind === "entra" && (
        <div>
          <Label htmlFor="idp-tenant">Tenant ID or domain</Label>
          <Input required placeholder="acme.onmicrosoft.com" {...field("tenant")} />
        </div>
      )}
      {p.kind === "okta" && (
        <div>
          <Label htmlFor="idp-domain">Okta domain</Label>
          <Input required placeholder="acme.okta.com" {...field("domain")} />
        </div>
      )}
      {p.kind === "oidc" && (
        <div>
          <Label htmlFor="idp-issuer">Issuer URL</Label>
          <Input required placeholder="https://login.acme.com" {...field("issuer")} />
        </div>
      )}
      {p.kind === "saml" && (
        <>
          <div>
            <Label htmlFor="idp-metadata_url">Metadata URL</Label>
            <Input
              required
              placeholder="https://idp.acme.com/metadata"
              {...field("metadata_url")}
            />
          </div>
          <div>
            <Label htmlFor="idp-email_attribute">Email attribute</Label>
            <Input placeholder="email" {...field("email_attribute")} />
          </div>
        </>
      )}
      {oidc && (
        <>
          <div>
            <Label htmlFor="idp-client_id">Client ID at the provider</Label>
            <Input required {...field("client_id")} />
          </div>
          <div>
            <Label htmlFor="idp-client_secret">Client secret at the provider</Label>
            <Input required type="password" autoComplete="off" {...field("client_secret")} />
          </div>
        </>
      )}
      <label className="flex gap-2 text-sm sm:col-span-2">
        <input
          type="checkbox"
          checked={p.link_by_email}
          onChange={(e) => setP({ ...p, link_by_email: e.target.checked })}
        />
        <span>
          <span className="font-medium">Link by email</span>
          <span className="block text-slate-500">
            People whose email already has a Grantra Login account sign in to it straight away. Only
            for a provider that verifies emails, such as your own Entra ID tenant or Okta org.
          </span>
        </span>
      </label>
      <div className="sm:col-span-2">
        <Button type="submit" disabled={create.isPending}>
          {create.isPending ? "Adding…" : "Add provider"}
        </Button>
      </div>
      <ErrorAlert error={create.error} className="sm:col-span-2" />
    </form>
  );
}

export function ImportFromKeycloak() {
  const client = useQueryClient();
  const [file, setFile] = useState<File | null>(null);
  const [plan, setPlan] = useState<ImportPlan | null>(null);
  const run = useMutation({
    mutationFn: (dryRun: boolean) => importToLogin(file!, dryRun),
    onSuccess: (p) => {
      setPlan(p);
      if (p.applied) void client.invalidateQueries({ queryKey: ["login"] });
    },
  });
  const adds = plan
    ? plan.users.add.length + plan.groups.add.length + plan.roles.add.length + plan.apps.add.length
    : 0;
  return (
    <Card>
      <CardHeader>
        <CardTitle>Import from Keycloak</CardTitle>
        <CardDescription>
          Bring users, groups, roles and apps from another Keycloak. People keep their passwords.
          Export the realm with{" "}
          <code>kc.sh export --realm acme --users realm_file --file acme.json</code>, check what the
          file would add, then import it. Nothing that exists already changes.
        </CardDescription>
      </CardHeader>
      <CardContent className="flex flex-col gap-3">
        <div className="flex flex-wrap items-center gap-2">
          <input
            type="file"
            accept=".json,application/json"
            aria-label="Keycloak realm export"
            onChange={(e) => {
              setFile(e.target.files?.[0] ?? null);
              setPlan(null);
            }}
            className="text-sm file:mr-3 file:rounded-md file:border file:border-slate-300 file:bg-white file:px-3 file:py-1.5 file:text-sm"
          />
          <Button
            variant="outline"
            disabled={!file || run.isPending}
            onClick={() => run.mutate(true)}
          >
            Check file
          </Button>
          <Button
            disabled={!plan || plan.applied || adds === 0 || run.isPending}
            onClick={() => {
              if (window.confirm(`Import ${plan!.realm} into Grantra Login?`)) run.mutate(false);
            }}
          >
            Import
          </Button>
        </div>
        <ErrorAlert error={run.error} />
        {plan && (
          <div className="flex flex-col gap-2 text-sm">
            {plan.applied ? (
              <Alert variant="success">Imported realm {plan.realm}.</Alert>
            ) : (
              <p>
                Realm <code>{plan.realm}</code>
                {adds === 0 ? ": Grantra Login already has everything in it." : " would add:"}
              </p>
            )}
            <ul className="list-disc pl-5">
              <Line label="users" set={plan.users} />
              <Line label="groups" set={plan.groups} />
              <Line label="roles" set={plan.roles} />
              <Line label="apps" set={plan.apps} />
            </ul>
            {plan.notes.map((n) => (
              <p key={n} className="text-slate-500">
                {n}
              </p>
            ))}
          </div>
        )}
      </CardContent>
    </Card>
  );
}

function Line({ label, set }: { label: string; set: ImportSet }) {
  if (set.add.length === 0 && set.skip.length === 0) return null;
  const sample = set.add.slice(0, 5).join(", ") + (set.add.length > 5 ? "…" : "");
  return (
    <li>
      {set.add.length} {label}
      {set.add.length > 0 && <span className="text-slate-500"> ({sample})</span>}
      {set.skip.length > 0 && `, ${set.skip.length} already there`}
    </li>
  );
}
