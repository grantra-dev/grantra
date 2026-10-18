import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { Link } from "react-router";
import { Loaded } from "@/components/Loaded";
import { Alert } from "@/components/ui/alert";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Input, Label, Textarea } from "@/components/ui/field";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import {
  createLoginApp,
  getLogin,
  rotateLoginAppSecret,
  setLoginPolicy,
  type LoginPolicy,
  type MFAPolicy,
  type SSOApp,
} from "@/lib/login";
import { IdentityProviders, ImportFromKeycloak } from "./GrantraLoginFederation";

const mfaChoices: { value: MFAPolicy; label: string; help: string }[] = [
  {
    value: "optional",
    label: "Only for people who set one up",
    help: "Keycloak's default: people may add an authenticator app, nobody has to.",
  },
  {
    value: "sensitive",
    label: "For people with sensitive access",
    help: "Whoever holds or is assigned high or critical risk access must use an authenticator app. Grantra keeps the list up to date as access changes.",
  },
  {
    value: "required",
    label: "For everyone",
    help: "Everyone uses an authenticator app; people without one set it up at their next sign-in.",
  },
];

export function GrantraLogin() {
  const { data, error, isPending } = useQuery({ queryKey: ["login"], queryFn: getLogin });
  return (
    <Loaded loading={isPending} error={error}>
      {data && !data.connected && (
        <Alert>
          Grantra Login is not connected yet. Give an application the <code>grantra-login</code>{" "}
          connector to manage the bundled sign-in from here. If people sign in through Entra ID,
          Okta or another provider, there is nothing to set up: Grantra governs on top of it.
        </Alert>
      )}
      {data?.connected && (
        <div className="flex flex-col gap-4">
          <p className="text-sm text-slate-600">
            Managed through{" "}
            <Link
              to={`/governance/applications/${data.application!.id}`}
              className="text-brand-700 hover:underline"
            >
              {data.application!.name}
            </Link>
            {data.realm && (
              <>
                , realm <code>{data.realm.realm}</code>
              </>
            )}
            .
          </p>
          {data.error && <Alert variant="error">{data.error}</Alert>}
          {data.policy && <SignInPolicy current={data.policy} />}
          {data.apps && <Apps apps={data.apps} />}
          {data.identity_providers && <IdentityProviders providers={data.identity_providers} />}
          {data.policy && <ImportFromKeycloak />}
        </div>
      )}
    </Loaded>
  );
}

function SignInPolicy({ current }: { current: LoginPolicy }) {
  const client = useQueryClient();
  const [policy, setPolicy] = useState(current);
  const save = useMutation({
    mutationFn: () => setLoginPolicy(policy),
    onSuccess: () => void client.invalidateQueries({ queryKey: ["login"] }),
  });
  const changed = policy.mfa !== current.mfa || policy.passkeys !== current.passkeys;
  return (
    <Card>
      <CardHeader>
        <CardTitle>Sign-in policy</CardTitle>
        <CardDescription>
          How people sign in to Grantra Login and every app behind it.
        </CardDescription>
      </CardHeader>
      <CardContent>
        <form
          className="flex flex-col gap-4"
          onSubmit={(e) => {
            e.preventDefault();
            save.mutate();
          }}
        >
          <fieldset className="flex flex-col gap-2">
            <legend className="mb-1 text-sm font-medium">Ask for a second factor</legend>
            {mfaChoices.map((c) => (
              <label key={c.value} className="flex gap-2 text-sm">
                <input
                  type="radio"
                  name="mfa"
                  value={c.value}
                  checked={policy.mfa === c.value}
                  onChange={() => setPolicy({ ...policy, mfa: c.value })}
                />
                <span>
                  <span className="font-medium">{c.label}</span>
                  <span className="block text-slate-500">{c.help}</span>
                </span>
              </label>
            ))}
          </fieldset>
          <label className="flex gap-2 text-sm">
            <input
              type="checkbox"
              checked={policy.passkeys}
              onChange={(e) => setPolicy({ ...policy, passkeys: e.target.checked })}
            />
            <span>
              <span className="font-medium">Passkeys</span>
              <span className="block text-slate-500">
                People can add a passkey from their account page and sign in with it, without a
                password or a code.
              </span>
            </span>
          </label>
          <div className="flex items-center gap-3">
            <Button type="submit" disabled={!changed || save.isPending}>
              {save.isPending ? "Saving…" : "Save policy"}
            </Button>
            {save.data && !save.isPending && !changed && (
              <span className="text-sm text-emerald-700">
                Saved
                {save.data.sync &&
                  `. ${save.data.sync.added} people now asked for a second factor, ${save.data.sync.removed} no longer.`}
              </span>
            )}
          </div>
          {save.data?.sync_error && (
            <Alert variant="warning">
              The policy is saved, but the list of people with sensitive access could not be updated
              yet: {save.data.sync_error}. Grantra tries again every few minutes.
            </Alert>
          )}
          {save.error && <Alert variant="error">{save.error.message}</Alert>}
        </form>
      </CardContent>
    </Card>
  );
}

function Apps({ apps }: { apps: SSOApp[] }) {
  const [secret, setSecret] = useState<{ app: string; value: string } | null>(null);
  const rotate = useMutation({
    mutationFn: (a: SSOApp) => rotateLoginAppSecret(a.id),
    onSuccess: (r, a) => setSecret({ app: a.name || a.client_id, value: r.secret }),
  });
  return (
    <Card>
      <CardHeader>
        <CardTitle>Apps that sign in with Grantra Login</CardTitle>
        <CardDescription>
          OpenID Connect apps registered in the realm. Each signs people in with the authorization
          code flow and PKCE.
        </CardDescription>
      </CardHeader>
      <CardContent className="flex flex-col gap-4">
        {secret && (
          <Alert variant="success">
            New secret for {secret.app}, shown only now:{" "}
            <code className="break-all">{secret.value}</code>
          </Alert>
        )}
        {rotate.error && <Alert variant="error">{rotate.error.message}</Alert>}
        {apps.length === 0 ? (
          <p className="text-sm text-slate-500">No apps yet.</p>
        ) : (
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>App</TableHead>
                <TableHead>Client ID</TableHead>
                <TableHead>Redirect URIs</TableHead>
                <TableHead />
              </TableRow>
            </TableHeader>
            <TableBody>
              {apps.map((a) => (
                <TableRow key={a.id}>
                  <TableCell>
                    <span className="font-medium">{a.name || a.client_id}</span>{" "}
                    {!a.enabled && <Badge>Disabled</Badge>}
                    {a.public && <Badge>Public</Badge>}
                  </TableCell>
                  <TableCell>
                    <code>{a.client_id}</code>
                  </TableCell>
                  <TableCell className="text-sm">
                    {a.redirect_uris.map((u) => (
                      <div key={u} className="break-all">
                        {u}
                      </div>
                    ))}
                  </TableCell>
                  <TableCell className="text-right">
                    {!a.public && (
                      <Button
                        variant="outline"
                        size="sm"
                        disabled={rotate.isPending}
                        onClick={() => {
                          if (
                            window.confirm(
                              `Give ${a.name || a.client_id} a new secret? The current one stops working right away.`,
                            )
                          )
                            rotate.mutate(a);
                        }}
                      >
                        New secret
                      </Button>
                    )}
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        )}
        <NewApp onSecret={(app, value) => setSecret({ app, value })} />
      </CardContent>
    </Card>
  );
}

function NewApp({ onSecret }: { onSecret: (app: string, secret: string) => void }) {
  const client = useQueryClient();
  const [name, setName] = useState("");
  const [clientId, setClientId] = useState("");
  const [uris, setUris] = useState("");
  const [isPublic, setPublic] = useState(false);
  const create = useMutation({
    mutationFn: () =>
      createLoginApp({
        name,
        client_id: clientId,
        public: isPublic,
        redirect_uris: uris
          .split("\n")
          .map((u) => u.trim())
          .filter(Boolean),
      }),
    onSuccess: (r) => {
      if (r.secret) onSecret(r.app.name, r.secret);
      setName("");
      setClientId("");
      setUris("");
      setPublic(false);
      void client.invalidateQueries({ queryKey: ["login"] });
    },
  });
  return (
    <form
      className="grid gap-3 border-t border-slate-200 pt-4 sm:grid-cols-2"
      onSubmit={(e) => {
        e.preventDefault();
        create.mutate();
      }}
    >
      <h4 className="text-sm font-medium sm:col-span-2">Register an app</h4>
      <div>
        <Label htmlFor="sso-name">Name</Label>
        <Input
          id="sso-name"
          required
          value={name}
          onChange={(e) => setName(e.target.value)}
          placeholder="Wiki"
        />
      </div>
      <div>
        <Label htmlFor="sso-client">Client ID</Label>
        <Input
          id="sso-client"
          required
          value={clientId}
          onChange={(e) => setClientId(e.target.value)}
          placeholder="wiki"
        />
      </div>
      <div className="sm:col-span-2">
        <Label htmlFor="sso-uris">Redirect URIs, one per line</Label>
        <Textarea
          id="sso-uris"
          required
          rows={2}
          value={uris}
          onChange={(e) => setUris(e.target.value)}
          placeholder="https://wiki.example.com/oauth/callback"
        />
      </div>
      <label className="flex gap-2 text-sm sm:col-span-2">
        <input type="checkbox" checked={isPublic} onChange={(e) => setPublic(e.target.checked)} />
        <span>
          Public app, without a secret (single-page and mobile apps). Leave it off for apps with a
          server.
        </span>
      </label>
      <div className="sm:col-span-2">
        <Button type="submit" disabled={create.isPending}>
          Register app
        </Button>
      </div>
      {create.error && (
        <Alert variant="error" className="sm:col-span-2">
          {create.error.message}
        </Alert>
      )}
    </form>
  );
}
