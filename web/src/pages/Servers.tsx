import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { Link, useSearchParams } from "react-router";
import { FormActions } from "@/components/FormActions";
import { Loaded } from "@/components/Loaded";
import { RecordedSearch, SessionBadge, SessionList } from "@/components/Sessions";
import { PageHeader } from "@/components/PageHeader";
import { RiskBadge } from "@/components/RiskBadge";
import { Tabs } from "@/components/Tabs";
import { Alert, ErrorAlert } from "@/components/ui/alert";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Input, Label, Select, Textarea } from "@/components/ui/field";
import { PlatformRole, type Me } from "@/lib/api";
import {
  connectCommand,
  createSSHTarget,
  deleteSSHTarget,
  getSSH,
  issueSSHCertificate,
  listSSHSessions,
  listSSHTargets,
  searchSSHCommands,
  terminateSSHSession,
  updateSSHTarget,
  type NewSSHTarget,
  type SSHCertificate,
  type SSHTarget,
} from "@/lib/ssh";
import { focusRing, useFocusParam } from "@/lib/focus";
import { cn, formatTime } from "@/lib/utils";

type Tab = "servers" | "sessions" | "commands";

export function Servers({ me }: { me: Me }) {
  const admin = me.platform_roles.includes(PlatformRole.Admin);
  const reader = admin || me.platform_roles.includes(PlatformRole.Auditor);
  const [params, setParams] = useSearchParams();
  const asked = params.get("tab");
  const tab: Tab = reader && (asked === "sessions" || asked === "commands") ? asked : "servers";
  return (
    <>
      <PageHeader
        title="Servers"
        description="Log in to servers over SSH with a certificate that lasts only as long as your access. Sessions through the gateway are recorded."
      />
      {reader && (
        <Tabs
          tabs={[
            ["servers", "Servers"],
            ["sessions", "Sessions"],
            ["commands", "Commands"],
          ]}
          current={tab}
          onSelect={(t) => setParams(t === "servers" ? {} : { tab: t })}
        />
      )}
      {tab === "servers" ? (
        <Targets admin={admin} reader={reader} />
      ) : tab === "sessions" ? (
        <Sessions admin={admin} />
      ) : (
        <Commands />
      )}
    </>
  );
}

function Targets({ admin, reader }: { admin: boolean; reader: boolean }) {
  const info = useQuery({ queryKey: ["ssh"], queryFn: getSSH });
  const { data, error, isPending } = useQuery({
    queryKey: ["ssh-targets"],
    queryFn: listSSHTargets,
  });
  const [adding, setAdding] = useState(false);
  const [open, setOpen] = useState<string | null>(null);
  // The search box links here with ?target=<id>.
  const asked = useFocusParam("target", !!data);
  const gateway = info.data?.gateway_address ?? "";
  return (
    <section className="flex flex-col gap-3">
      {info.data && !info.data.enabled && (
        <Alert variant="warning">
          Grantra cannot sign SSH certificates yet: an operator has to set GRANTRA_VAULT_KEY and
          restart Grantra.
        </Alert>
      )}
      {admin && (
        <div className="flex justify-end">
          {!adding && (
            <Button variant="outline" onClick={() => setAdding(true)}>
              Add server
            </Button>
          )}
        </div>
      )}
      {adding && <NewTarget onDone={() => setAdding(false)} />}
      <Loaded
        loading={isPending}
        error={error}
        empty={
          data?.length === 0 &&
          (reader
            ? "No servers yet."
            : "You hold no SSH access right now. Request it under Request access, or activate it from My access.")
        }
      >
        <ul className="divide-y divide-slate-200">
          {data?.map((t) => (
            <TargetRow
              key={t.id}
              t={t}
              admin={admin}
              gateway={gateway}
              focused={asked === t.id}
              open={open === t.id}
              onToggle={() => setOpen(open === t.id ? null : t.id)}
            />
          ))}
        </ul>
      </Loaded>
      {admin && info.data?.ca_public_key && (
        <TrustCA people={info.data.ca_public_key} gateway={info.data.gateway_ca_public_key} />
      )}
    </section>
  );
}

function TargetRow({
  t,
  admin,
  gateway,
  focused,
  open,
  onToggle,
}: {
  t: SSHTarget;
  admin: boolean;
  gateway: string;
  focused: boolean;
  open: boolean;
  onToggle: () => void;
}) {
  const [connecting, setConnecting] = useState(false);
  return (
    <li data-focus={t.id} className={cn("p-4", focused && focusRing)}>
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div className="min-w-0">
          <div className="flex flex-wrap items-center gap-2">
            <span className="font-medium">{t.name}</span>
            <RiskBadge risk={t.risk} />
            {t.via_gateway ? <Badge variant="brand">Recorded</Badge> : <Badge>Direct</Badge>}
          </div>
          <div className="text-sm text-slate-500">
            {t.login} on {t.address} · certificates last up to {t.max_certificate_hours}{" "}
            {t.max_certificate_hours === 1 ? "hour" : "hours"}
          </div>
          {t.notes && <p className="mt-1 text-sm text-slate-700">{t.notes}</p>}
        </div>
        <div className="flex gap-2">
          {t.held && !connecting && (
            <Button size="sm" onClick={() => setConnecting(true)}>
              Get certificate
            </Button>
          )}
          {admin && (
            <Button size="sm" variant="outline" onClick={onToggle}>
              {open ? "Close" : "Settings"}
            </Button>
          )}
        </div>
      </div>
      {connecting && (
        <GetCertificate t={t} gateway={gateway} onClose={() => setConnecting(false)} />
      )}
      {open && <TargetSettings t={t} onDone={onToggle} />}
    </li>
  );
}

function GetCertificate({
  t,
  gateway,
  onClose,
}: {
  t: SSHTarget;
  gateway: string;
  onClose: () => void;
}) {
  const [key, setKey] = useState("");
  const [cert, setCert] = useState<SSHCertificate | null>(null);
  const issue = useMutation({
    mutationFn: () => issueSSHCertificate(t.id, key.trim()),
    onSuccess: setCert,
  });
  if (cert) {
    return (
      <div className="mt-3 flex flex-col gap-2 rounded-md border border-slate-200 bg-slate-50 p-3 text-sm">
        <p>
          Valid until <strong>{formatTime(cert.valid_before)}</strong>. Save it next to your key as{" "}
          <code>~/.ssh/id_ed25519-cert.pub</code> (same name as the key, ending in{" "}
          <code>-cert.pub</code>), then connect:
        </p>
        <Label htmlFor={`cert-${t.id}`}>Certificate</Label>
        <Textarea
          id={`cert-${t.id}`}
          readOnly
          rows={4}
          value={cert.certificate}
          className="font-mono text-xs"
          onFocus={(e) => e.currentTarget.select()}
        />
        <pre className="rounded bg-slate-900 p-2 text-xs text-slate-100">
          {connectCommand(t, gateway)}
        </pre>
        {t.via_gateway && !gateway && (
          <p className="text-slate-500">Ask your Grantra admin for the gateway address.</p>
        )}
        <div>
          <Button size="sm" variant="outline" onClick={onClose}>
            Done
          </Button>
        </div>
      </div>
    );
  }
  return (
    <form
      className="mt-3 flex flex-col gap-2"
      onSubmit={(e) => {
        e.preventDefault();
        issue.mutate();
      }}
    >
      <Label htmlFor={`key-${t.id}`}>Your public key</Label>
      <Textarea
        id={`key-${t.id}`}
        rows={2}
        value={key}
        onChange={(e) => setKey(e.target.value)}
        placeholder="ssh-ed25519 AAAA… you@laptop (the contents of ~/.ssh/id_ed25519.pub)"
        className="font-mono text-xs"
      />
      <p className="text-xs text-slate-500">
        Only the public key: it ends in .pub. No key yet? Run <code>ssh-keygen -t ed25519</code>.
      </p>
      <div className="flex gap-2">
        <Button type="submit" size="sm" disabled={!key.trim() || issue.isPending}>
          Sign certificate
        </Button>
        <Button type="button" size="sm" variant="outline" onClick={onClose}>
          Cancel
        </Button>
      </div>
      <ErrorAlert error={issue.error} />
    </form>
  );
}

function TargetSettings({ t, onDone }: { t: SSHTarget; onDone: () => void }) {
  const client = useQueryClient();
  const [address, setAddress] = useState(t.address);
  const [hours, setHours] = useState(t.max_certificate_hours);
  const [viaGateway, setViaGateway] = useState(t.via_gateway);
  const [notes, setNotes] = useState(t.notes ?? "");
  const refresh = () => client.invalidateQueries({ queryKey: ["ssh-targets"] });
  const save = useMutation({
    mutationFn: (forget: boolean) =>
      updateSSHTarget(t.id, {
        address,
        notes,
        max_certificate_hours: hours,
        via_gateway: viaGateway,
        forget_host_key: forget,
      }),
    onSuccess: refresh,
  });
  const remove = useMutation({
    mutationFn: () => deleteSSHTarget(t.id),
    onSuccess: () => {
      onDone();
      return refresh();
    },
  });
  return (
    <form
      className="mt-3 grid gap-3 rounded-md border border-slate-200 p-3 sm:grid-cols-2"
      onSubmit={(e) => {
        e.preventDefault();
        save.mutate(false);
      }}
    >
      <div>
        <Label htmlFor={`addr-${t.id}`}>Address</Label>
        <Input id={`addr-${t.id}`} value={address} onChange={(e) => setAddress(e.target.value)} />
      </div>
      <div>
        <Label htmlFor={`hours-${t.id}`}>Certificates last up to (hours)</Label>
        <Input
          id={`hours-${t.id}`}
          type="number"
          min={1}
          max={24}
          value={hours}
          onChange={(e) => setHours(Number(e.target.value))}
        />
      </div>
      <div>
        <Label htmlFor={`via-${t.id}`}>Connections</Label>
        <Select
          id={`via-${t.id}`}
          value={viaGateway ? "gateway" : "direct"}
          onChange={(e) => setViaGateway(e.target.value === "gateway")}
        >
          <option value="gateway">Through the gateway, recorded</option>
          <option value="direct">Straight to the server, not recorded</option>
        </Select>
      </div>
      <div>
        <Label htmlFor={`notes-${t.id}`}>Notes</Label>
        <Input id={`notes-${t.id}`} value={notes} onChange={(e) => setNotes(e.target.value)} />
      </div>
      <p className="text-sm text-slate-500 sm:col-span-2">
        Host key:{" "}
        {t.host_key ? (
          <code className="break-all">{t.host_key}</code>
        ) : (
          "learned on the gateway's first connection."
        )}
      </p>
      <div className="flex flex-wrap gap-2 sm:col-span-2">
        <Button type="submit" size="sm" disabled={save.isPending}>
          Save
        </Button>
        {t.host_key && (
          <Button
            type="button"
            size="sm"
            variant="outline"
            onClick={() => save.mutate(true)}
            disabled={save.isPending}
          >
            Forget host key
          </Button>
        )}
        <Button
          type="button"
          size="sm"
          variant="destructive"
          onClick={() => {
            if (window.confirm(`Delete ${t.name}? Everyone loses access to it; recordings stay.`))
              remove.mutate();
          }}
          disabled={remove.isPending}
        >
          Delete
        </Button>
      </div>
      <ErrorAlert error={save.error ?? remove.error} className="sm:col-span-2" />
    </form>
  );
}

function NewTarget({ onDone }: { onDone: () => void }) {
  const client = useQueryClient();
  const [t, setT] = useState<NewSSHTarget>({ name: "", login: "", address: "", risk: "high" });
  const set = (patch: Partial<NewSSHTarget>) => setT({ ...t, ...patch });
  const create = useMutation({
    mutationFn: () => createSSHTarget(t),
    onSuccess: () => {
      onDone();
      return client.invalidateQueries({ queryKey: ["ssh-targets"] });
    },
  });
  return (
    <Card>
      <CardHeader>
        <CardTitle>Add a server</CardTitle>
        <p className="text-sm text-slate-500">
          Grantra adds the access to log in to it to the catalog, just in time only: people are made
          eligible and activate it, or request it for a time.
        </p>
      </CardHeader>
      <CardContent>
        <form
          className="grid gap-3 sm:grid-cols-2"
          onSubmit={(e) => {
            e.preventDefault();
            create.mutate();
          }}
        >
          <div>
            <Label htmlFor="ssh-name">Name</Label>
            <Input
              id="ssh-name"
              value={t.name}
              onChange={(e) => set({ name: e.target.value })}
              placeholder="prod-db"
              required
            />
          </div>
          <div>
            <Label htmlFor="ssh-address">Address</Label>
            <Input
              id="ssh-address"
              value={t.address}
              onChange={(e) => set({ address: e.target.value })}
              placeholder="10.0.0.5:22"
              required
            />
          </div>
          <div>
            <Label htmlFor="ssh-login">Log in as</Label>
            <Input
              id="ssh-login"
              value={t.login}
              onChange={(e) => set({ login: e.target.value })}
              placeholder="postgres"
              required
            />
          </div>
          <div>
            <Label htmlFor="ssh-risk">Risk</Label>
            <Select id="ssh-risk" value={t.risk} onChange={(e) => set({ risk: e.target.value })}>
              <option value="high">High</option>
              <option value="critical">Critical</option>
            </Select>
          </div>
          <FormActions
            action={create}
            submit="Add server"
            onCancel={onDone}
            className="sm:col-span-2"
          />
        </form>
      </CardContent>
    </Card>
  );
}

function TrustCA({ people, gateway }: { people: string; gateway: string }) {
  return (
    <Card>
      <CardHeader>
        <CardTitle>Make a server trust Grantra</CardTitle>
        <p className="text-sm text-slate-500">
          Once per server: save the key for how it is reached as{" "}
          <code>/etc/ssh/grantra_ca.pub</code>, add{" "}
          <code>TrustedUserCAKeys /etc/ssh/grantra_ca.pub</code> to <code>sshd_config</code> and
          reload sshd. No personal keys go on the server.
        </p>
      </CardHeader>
      <CardContent className="flex flex-col gap-4">
        <div className="flex flex-col gap-1">
          <Label htmlFor="gateway-ca">Through the gateway (recorded)</Label>
          <p className="text-sm text-slate-500">
            Only the gateway logs in to these servers: nobody gets around it.
          </p>
          <Textarea
            id="gateway-ca"
            readOnly
            rows={2}
            value={gateway}
            className="font-mono text-xs"
            onFocus={(e) => e.currentTarget.select()}
          />
        </div>
        <div className="flex flex-col gap-1">
          <Label htmlFor="people-ca">Reached directly</Label>
          <p className="text-sm text-slate-500">
            Also add <code>AuthorizedPrincipalsFile /etc/ssh/grantra_principals/%u</code>, and in
            that file, for each account, one line <code>grantra:&lt;server name&gt;</code> per
            server in Grantra that logs in as it.
          </p>
          <Textarea
            id="people-ca"
            readOnly
            rows={2}
            value={people}
            className="font-mono text-xs"
            onFocus={(e) => e.currentTarget.select()}
          />
        </div>
      </CardContent>
    </Card>
  );
}

function Sessions({ admin }: { admin: boolean }) {
  return (
    <SessionList
      name="ssh-sessions"
      list={listSSHSessions}
      terminate={terminateSSHSession}
      admin={admin}
      columns={[
        [
          "Server",
          (s) => (
            <>
              {s.target_name} as {s.login}
              {s.kind === "exec" && (
                <div className="font-mono text-xs text-slate-500">{s.command}</div>
              )}
            </>
          ),
        ],
        ["Started", (s) => formatTime(s.started_at)],
      ]}
      status={(s) => <SessionBadge s={s} />}
      link={(s) => (
        <Link to={`/servers/sessions/${s.id}`} className="text-sm text-brand-700 underline">
          Watch
        </Link>
      )}
    />
  );
}

function Commands() {
  return (
    <RecordedSearch
      name="ssh-commands"
      search={searchSSHCommands}
      label="Command"
      placeholder="DROP TABLE"
      note="Commands typed through the gateway, best effort: text recalled from shell history or completed with Tab may differ. The recording is the record."
      empty={(asked) => `Nobody typed “${asked}”.`}
      columns={[
        [
          "Server",
          (c) => (
            <>
              {c.target_name} as {c.login}
            </>
          ),
        ],
        [
          "Command",
          (c) => (
            <Link
              to={`/servers/sessions/${c.session_id}?t=${Math.floor(c.offset_seconds)}`}
              className="font-mono text-xs text-brand-700 underline"
            >
              {c.command}
            </Link>
          ),
        ],
      ]}
    />
  );
}
