import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { Link } from "react-router";
import { Loaded } from "@/components/Loaded";
import { PageHeader } from "@/components/PageHeader";
import { RiskBadge } from "@/components/RiskBadge";
import { Tabs } from "@/components/Tabs";
import { Alert } from "@/components/ui/alert";
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
import { PlatformRole, type Me } from "@/lib/api";
import { formatTime } from "@/lib/utils";
import {
  checkinVault,
  checkoutVaultAccount,
  createVaultAccount,
  deleteVaultAccount,
  discoverVaultAccounts,
  getVaultAccount,
  listVaultAccounts,
  rotateVaultAccount,
  setVaultSecret,
  updateVaultAccount,
  type NewVaultAccount,
  type VaultAccount,
  type VaultFinding,
  type VaultLease,
  type VaultSettings,
} from "@/lib/vault";

type Tab = "accounts" | "discovery";

export function Vault({ me }: { me: Me }) {
  const admin = me.platform_roles.includes(PlatformRole.Admin);
  const reader = admin || me.platform_roles.includes(PlatformRole.Auditor);
  const [tab, setTab] = useState<Tab>("accounts");
  const [adding, setAdding] = useState<Partial<NewVaultAccount> | null>(null);
  return (
    <>
      <PageHeader
        title="Vault"
        description="Passwords and keys of privileged accounts. Check one out when you need it; Grantra records who, when and why, and can change the password after."
      />
      {reader && (
        <Tabs
          tabs={[
            ["accounts", "Accounts"],
            ["discovery", "Discovery"],
          ]}
          current={tab}
          onSelect={setTab}
        />
      )}
      {tab === "accounts" || !reader ? (
        <Accounts me={me} admin={admin} reader={reader} adding={adding} setAdding={setAdding} />
      ) : (
        <Discovery
          admin={admin}
          onAdd={(f) => {
            setAdding({
              name: `${f.application_name} ${f.username || f.native_id}`,
              account_id: f.account_id,
            });
            setTab("accounts");
          }}
        />
      )}
    </>
  );
}

function Accounts({
  me,
  admin,
  reader,
  adding,
  setAdding,
}: {
  me: Me;
  admin: boolean;
  reader: boolean;
  adding: Partial<NewVaultAccount> | null;
  setAdding: (a: Partial<NewVaultAccount> | null) => void;
}) {
  const { data, error, isPending } = useQuery({
    queryKey: ["vault-accounts"],
    queryFn: listVaultAccounts,
  });
  const [open, setOpen] = useState<string | null>(null);
  return (
    <section className="flex flex-col gap-3">
      {data && !data.enabled && (
        <Alert variant="warning">
          The vault cannot store or hand out secrets yet: an operator has to set GRANTRA_VAULT_KEY
          and restart Grantra.
        </Alert>
      )}
      {admin && (
        <div className="flex justify-end">
          {!adding && (
            <Button variant="outline" onClick={() => setAdding({})}>
              Add account
            </Button>
          )}
        </div>
      )}
      {adding && <NewAccount initial={adding} onDone={() => setAdding(null)} />}
      <Loaded
        loading={isPending}
        error={error}
        empty={
          data?.accounts.length === 0 &&
          (reader
            ? "No accounts in the vault yet."
            : "You hold no vault access. Request it like any other access, under Request access.")
        }
      >
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>Account</TableHead>
              <TableHead>Secret</TableHead>
              <TableHead>Checked out</TableHead>
              <TableHead className="relative">
                <span className="sr-only">Actions</span>
              </TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {data?.accounts.map((a) => (
              <AccountRow
                key={a.id}
                account={a}
                me={me}
                reader={reader}
                expanded={open === a.id}
                onToggle={() => setOpen(open === a.id ? null : a.id)}
              />
            ))}
          </TableBody>
        </Table>
      </Loaded>
      {open && reader && (
        <AccountDetail
          id={open}
          admin={admin}
          account={data?.accounts.find((a) => a.id === open)}
          onClose={() => setOpen(null)}
        />
      )}
    </section>
  );
}

function rotationText(a: VaultAccount) {
  if (a.kind !== "password") return null;
  if (a.rotation_failures > 0) return null;
  const parts = [];
  if (a.rotate_every_days) parts.push(`every ${a.rotate_every_days} days`);
  if (a.rotate_after_checkin) parts.push("after each use");
  return parts.length ? `Rotates ${parts.join(" and ")}` : null;
}

function AccountRow({
  account: a,
  me,
  reader,
  expanded,
  onToggle,
}: {
  account: VaultAccount;
  me: Me;
  reader: boolean;
  expanded: boolean;
  onToggle: () => void;
}) {
  const [checkingOut, setCheckingOut] = useState(false);
  const mine = a.checkouts.find((c) => c.identity_id === me.identity_id);
  const others = a.checkouts.filter((c) => c.identity_id !== me.identity_id);
  return (
    <>
      <TableRow className="align-top">
        <TableCell>
          <div className="flex flex-wrap items-center gap-2 font-medium">
            {reader ? (
              <button
                className="text-left hover:underline"
                onClick={onToggle}
                aria-expanded={expanded}
              >
                {a.name}
              </button>
            ) : (
              a.name
            )}
            <RiskBadge risk={a.risk} />
            {a.exclusive && <Badge>One at a time</Badge>}
          </div>
          <div className="text-slate-500">
            {a.username}
            {a.address && ` on ${a.address}`}
            {a.application_name && ` · ${a.application_name}`}
          </div>
          {a.notes && <div className="text-slate-500">{a.notes}</div>}
        </TableCell>
        <TableCell>
          {a.version === 0 ? (
            <Badge variant="amber">No secret yet</Badge>
          ) : (
            <div>
              {a.kind === "ssh_key" ? "SSH key" : "Password"}
              {a.secret_at && (
                <span className="text-slate-500"> · set {formatTime(a.secret_at)}</span>
              )}
            </div>
          )}
          {rotationText(a) && <div className="text-slate-500">{rotationText(a)}</div>}
          {a.rotation_failures > 0 && (
            <Badge variant="red" title={a.last_rotation_error}>
              Rotation failing
            </Badge>
          )}
        </TableCell>
        <TableCell>
          {a.checkouts.length === 0 && <span className="text-slate-500">No</span>}
          {mine && <div>By you until {formatTime(mine.expires_at)}</div>}
          {others.map((c) => (
            <div key={c.id}>
              {c.identity_name} until {formatTime(c.expires_at)}
              {c.reason && <div className="text-slate-500">{c.reason}</div>}
            </div>
          ))}
        </TableCell>
        <TableCell className="text-right">
          {a.held && a.version > 0 && !checkingOut && (
            <Button size="sm" onClick={() => setCheckingOut(true)}>
              {mine ? "Show again" : "Check out"}
            </Button>
          )}
        </TableCell>
      </TableRow>
      {checkingOut && (
        <TableRow>
          <TableCell colSpan={4}>
            <CheckOut account={a} onDone={() => setCheckingOut(false)} />
          </TableCell>
        </TableRow>
      )}
    </>
  );
}

function CheckOut({ account, onDone }: { account: VaultAccount; onDone: () => void }) {
  const client = useQueryClient();
  const [reason, setReason] = useState("");
  const [lease, setLease] = useState<VaultLease | null>(null);
  const [copied, setCopied] = useState(false);
  const refresh = () => void client.invalidateQueries({ queryKey: ["vault-accounts"] });
  const checkout = useMutation({
    mutationFn: () => checkoutVaultAccount(account.id, reason),
    onSuccess: (l) => {
      setLease(l);
      refresh();
    },
  });
  const checkin = useMutation({
    mutationFn: () => checkinVault(lease!.id),
    onSuccess: () => {
      setLease(null);
      refresh();
      onDone();
    },
  });
  if (lease) {
    return (
      <div className="flex flex-col gap-2">
        <Label htmlFor="vault-secret">
          {account.kind === "ssh_key" ? "Private key" : "Password"} for {account.username}, yours
          until {formatTime(lease.expires_at)}
        </Label>
        <Textarea
          id="vault-secret"
          readOnly
          value={lease.secret}
          rows={account.kind === "ssh_key" ? 8 : 1}
          className="font-mono"
          onFocus={(e) => e.currentTarget.select()}
        />
        <div className="flex flex-wrap gap-2">
          <Button
            size="sm"
            variant="outline"
            onClick={() =>
              void navigator.clipboard?.writeText(lease.secret).then(() => setCopied(true))
            }
          >
            {copied ? "Copied" : "Copy"}
          </Button>
          <Button size="sm" disabled={checkin.isPending} onClick={() => checkin.mutate()}>
            Check in, I am done
          </Button>
          <Button size="sm" variant="ghost" onClick={onDone}>
            Hide
          </Button>
        </div>
        {account.rotate_after_checkin && (
          <p className="text-xs text-slate-500">
            Grantra changes this password once nobody has it checked out.
          </p>
        )}
        {checkin.error && <Alert variant="error">{checkin.error.message}</Alert>}
      </div>
    );
  }
  return (
    <form
      className="flex flex-col gap-2"
      onSubmit={(e) => {
        e.preventDefault();
        checkout.mutate();
      }}
    >
      <Label htmlFor="vault-reason">Why do you need {account.name}?</Label>
      <Input
        id="vault-reason"
        required
        maxLength={1000}
        value={reason}
        onChange={(e) => setReason(e.target.value)}
        placeholder="INC-1234: restart the replica"
      />
      <p className="text-xs text-slate-500">
        It stays checked out for {account.checkout_minutes} minutes. Your reason goes to the audit
        log.
      </p>
      <div className="flex gap-2">
        <Button size="sm" type="submit" disabled={checkout.isPending}>
          Check out
        </Button>
        <Button size="sm" variant="ghost" type="button" onClick={onDone}>
          Cancel
        </Button>
      </div>
      {checkout.error && <Alert variant="error">{checkout.error.message}</Alert>}
    </form>
  );
}

const emptySettings: VaultSettings = {
  address: "",
  notes: "",
  checkout_minutes: 60,
  exclusive: false,
  rotate_every_days: 0,
  rotate_after_checkin: false,
};

function SettingsFields({
  value,
  onChange,
  kind,
}: {
  value: VaultSettings;
  onChange: (v: VaultSettings) => void;
  kind: string;
}) {
  return (
    <>
      <div>
        <Label htmlFor="vault-address">Where it is used</Label>
        <Input
          id="vault-address"
          value={value.address}
          placeholder="prod-db.example.com:5432"
          onChange={(e) => onChange({ ...value, address: e.target.value })}
        />
      </div>
      <div>
        <Label htmlFor="vault-minutes">Check-out lasts (minutes)</Label>
        <Input
          id="vault-minutes"
          type="number"
          min={5}
          max={480}
          value={value.checkout_minutes}
          onChange={(e) => onChange({ ...value, checkout_minutes: Number(e.target.value) })}
        />
      </div>
      {kind === "password" && (
        <div>
          <Label htmlFor="vault-days">Rotate every (days, 0 for never)</Label>
          <Input
            id="vault-days"
            type="number"
            min={0}
            max={365}
            value={value.rotate_every_days}
            onChange={(e) => onChange({ ...value, rotate_every_days: Number(e.target.value) })}
          />
        </div>
      )}
      <div className="flex flex-col gap-1 sm:col-span-2">
        <label className="flex items-center gap-2 text-sm">
          <input
            type="checkbox"
            checked={value.exclusive}
            onChange={(e) => onChange({ ...value, exclusive: e.target.checked })}
          />
          One person at a time
        </label>
        {kind === "password" && (
          <label className="flex items-center gap-2 text-sm">
            <input
              type="checkbox"
              checked={value.rotate_after_checkin}
              onChange={(e) => onChange({ ...value, rotate_after_checkin: e.target.checked })}
            />
            Change the password after each use
          </label>
        )}
      </div>
      <div className="sm:col-span-2">
        <Label htmlFor="vault-notes">Notes</Label>
        <Textarea
          id="vault-notes"
          value={value.notes}
          onChange={(e) => onChange({ ...value, notes: e.target.value })}
        />
      </div>
    </>
  );
}

function NewAccount({
  initial,
  onDone,
}: {
  initial: Partial<NewVaultAccount>;
  onDone: () => void;
}) {
  const client = useQueryClient();
  const [a, setA] = useState<NewVaultAccount>({
    ...emptySettings,
    name: "",
    username: "",
    kind: "password",
    risk: "critical",
    account_id: "",
    secret: "",
    ...initial,
  });
  const create = useMutation({
    mutationFn: () => createVaultAccount(a),
    onSuccess: () => {
      void client.invalidateQueries({ queryKey: ["vault-accounts"] });
      void client.invalidateQueries({ queryKey: ["vault-discovery"] });
      onDone();
    },
  });
  return (
    <Card>
      <CardHeader>
        <CardTitle>Add an account to the vault</CardTitle>
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
            <Label htmlFor="vault-name">Name</Label>
            <Input
              id="vault-name"
              required
              value={a.name}
              placeholder="prod-db root"
              onChange={(e) => setA({ ...a, name: e.target.value })}
            />
          </div>
          <div>
            <Label htmlFor="vault-username">Username</Label>
            <Input
              id="vault-username"
              required={!a.account_id}
              value={a.username}
              placeholder={a.account_id ? "The account's own" : "root"}
              onChange={(e) => setA({ ...a, username: e.target.value })}
            />
          </div>
          <div>
            <Label htmlFor="vault-kind">Secret</Label>
            <Select
              id="vault-kind"
              value={a.kind}
              onChange={(e) => setA({ ...a, kind: e.target.value as NewVaultAccount["kind"] })}
            >
              <option value="password">Password</option>
              <option value="ssh_key">SSH private key</option>
            </Select>
          </div>
          <div>
            <Label htmlFor="vault-risk">Risk</Label>
            <Select
              id="vault-risk"
              value={a.risk}
              onChange={(e) => setA({ ...a, risk: e.target.value as NewVaultAccount["risk"] })}
            >
              <option value="critical">Critical</option>
              <option value="high">High</option>
            </Select>
          </div>
          <div className="sm:col-span-2">
            <Label htmlFor="vault-value">
              {a.kind === "ssh_key" ? "Private key" : "Current password"}
            </Label>
            <Textarea
              id="vault-value"
              className="font-mono"
              rows={a.kind === "ssh_key" ? 6 : 1}
              value={a.secret}
              onChange={(e) => setA({ ...a, secret: e.target.value })}
            />
            <p className="mt-1 text-xs text-slate-500">
              Optional. Grantra stores it encrypted and never shows it to admins; leave it empty to
              set it later or to let a rotation pick one.
            </p>
          </div>
          <SettingsFields value={a} kind={a.kind} onChange={(s) => setA({ ...a, ...s })} />
          <p className="text-sm text-slate-500 sm:col-span-2">
            Grantra adds the access “Use {a.name || "…"}” to the catalog: people request it,
            approvers approve it, and only those who hold it can check the account out.
          </p>
          <div className="flex gap-2 sm:col-span-2">
            <Button type="submit" disabled={create.isPending}>
              Add
            </Button>
            <Button type="button" variant="ghost" onClick={onDone}>
              Cancel
            </Button>
          </div>
          {create.error && (
            <Alert variant="error" className="sm:col-span-2">
              {create.error.message}
            </Alert>
          )}
        </form>
      </CardContent>
    </Card>
  );
}

const endedLabels = { checked_in: "Checked in", expired: "Expired", ended: "Ended by an admin" };

function AccountDetail({
  id,
  admin,
  account,
  onClose,
}: {
  id: string;
  admin: boolean;
  account?: VaultAccount;
  onClose: () => void;
}) {
  const client = useQueryClient();
  const { data, error, isPending } = useQuery({
    queryKey: ["vault-account", id],
    queryFn: () => getVaultAccount(id),
  });
  const refresh = () => {
    void client.invalidateQueries({ queryKey: ["vault-accounts"] });
    void client.invalidateQueries({ queryKey: ["vault-account", id] });
  };
  const [secret, setSecret] = useState("");
  const [settings, setSettings] = useState<VaultSettings | null>(null);
  const save = useMutation({
    mutationFn: (s: VaultSettings) => updateVaultAccount(id, s),
    onSuccess: () => {
      setSettings(null);
      refresh();
    },
  });
  const set = useMutation({
    mutationFn: () => setVaultSecret(id, secret),
    onSuccess: () => {
      setSecret("");
      refresh();
    },
  });
  const rotate = useMutation({ mutationFn: () => rotateVaultAccount(id), onSuccess: refresh });
  const end = useMutation({ mutationFn: checkinVault, onSuccess: refresh });
  const remove = useMutation({
    mutationFn: () => deleteVaultAccount(id),
    onSuccess: () => {
      onClose();
      void client.invalidateQueries({ queryKey: ["vault-accounts"] });
    },
  });
  const a = data ?? account;
  return (
    <Card>
      <CardHeader className="flex flex-row items-center justify-between">
        <CardTitle>{a?.name}</CardTitle>
        <Button size="sm" variant="ghost" onClick={onClose}>
          Close
        </Button>
      </CardHeader>
      <CardContent className="flex flex-col gap-4 text-sm">
        {isPending && <p className="text-slate-500">Loading…</p>}
        {error && <Alert variant="error">{error.message}</Alert>}
        {data && (
          <>
            <p className="text-slate-500">
              Access to it is{" "}
              <Link className="underline" to="/request">
                {data.entitlement_name}
              </Link>
              , which people request like any other access.
            </p>
            {data.last_rotation_error && (
              <Alert variant="error">Last rotation failed: {data.last_rotation_error}</Alert>
            )}
            {admin && (
              <div className="flex flex-col gap-3">
                <form
                  className="flex flex-wrap items-end gap-2"
                  onSubmit={(e) => {
                    e.preventDefault();
                    set.mutate();
                  }}
                >
                  <div className="min-w-60 flex-1">
                    <Label htmlFor="vault-new-secret">Type a new secret</Label>
                    <Input
                      id="vault-new-secret"
                      type="password"
                      autoComplete="off"
                      required
                      value={secret}
                      onChange={(e) => setSecret(e.target.value)}
                    />
                  </div>
                  <Button type="submit" variant="outline" disabled={set.isPending}>
                    Save secret
                  </Button>
                  {data.can_rotate && (
                    <Button
                      type="button"
                      variant="outline"
                      disabled={rotate.isPending}
                      onClick={() => rotate.mutate()}
                    >
                      Rotate now
                    </Button>
                  )}
                  <Button
                    type="button"
                    variant="outline"
                    onClick={() =>
                      setSettings({
                        address: data.address ?? "",
                        notes: data.notes ?? "",
                        checkout_minutes: data.checkout_minutes,
                        exclusive: data.exclusive,
                        rotate_every_days: data.rotate_every_days ?? 0,
                        rotate_after_checkin: data.rotate_after_checkin,
                      })
                    }
                  >
                    Settings
                  </Button>
                  <Button
                    type="button"
                    variant="ghost"
                    disabled={remove.isPending}
                    onClick={() => {
                      if (
                        window.confirm(
                          `Delete ${data.name}? Its secrets and the access to it go with it.`,
                        )
                      ) {
                        remove.mutate();
                      }
                    }}
                  >
                    Delete
                  </Button>
                </form>
                {rotate.data && !rotate.data.rotated && (
                  <Alert variant="error">Rotation failed: {rotate.data.error}</Alert>
                )}
                {rotate.data?.rotated && (
                  <Alert variant="success">Rotated to version {rotate.data.version}.</Alert>
                )}
                {[set, rotate, remove, end].map(
                  (m, i) =>
                    m.error && (
                      <Alert key={i} variant="error">
                        {m.error.message}
                      </Alert>
                    ),
                )}
                {settings && (
                  <form
                    className="grid gap-3 sm:grid-cols-2"
                    onSubmit={(e) => {
                      e.preventDefault();
                      save.mutate(settings);
                    }}
                  >
                    <SettingsFields value={settings} kind={data.kind} onChange={setSettings} />
                    {!data.can_rotate && data.kind === "password" && (
                      <p className="text-xs text-slate-500 sm:col-span-2">
                        Rotation needs the account linked to an application whose connector can set
                        passwords (LDAP or Grantra Login); add it from Discovery.
                      </p>
                    )}
                    <div className="flex gap-2 sm:col-span-2">
                      <Button type="submit" disabled={save.isPending}>
                        Save
                      </Button>
                      <Button type="button" variant="ghost" onClick={() => setSettings(null)}>
                        Cancel
                      </Button>
                    </div>
                    {save.error && (
                      <Alert variant="error" className="sm:col-span-2">
                        {save.error.message}
                      </Alert>
                    )}
                  </form>
                )}
              </div>
            )}
            <div>
              <h3 className="mb-2 font-semibold">Check-outs</h3>
              {data.recent_checkouts.length === 0 ? (
                <p className="text-slate-500">Nobody has checked it out yet.</p>
              ) : (
                <ul className="flex flex-col gap-2">
                  {data.recent_checkouts.map((c) => (
                    <li key={c.id} className="flex flex-wrap items-center justify-between gap-2">
                      <div>
                        <span className="font-medium">{c.identity_name}</span>,{" "}
                        {formatTime(c.checked_out_at)}: {c.reason}
                        <div className="text-slate-500">
                          {c.ended_by
                            ? `${endedLabels[c.ended_by]} ${formatTime(c.ended_at!)}`
                            : `Out until ${formatTime(c.expires_at)}`}
                        </div>
                      </div>
                      {admin && !c.ended_at && (
                        <Button
                          size="sm"
                          variant="outline"
                          disabled={end.isPending}
                          onClick={() => end.mutate(c.id)}
                        >
                          End now
                        </Button>
                      )}
                    </li>
                  ))}
                </ul>
              )}
            </div>
            <div>
              <h3 className="mb-2 font-semibold">Versions</h3>
              <ul className="flex flex-col gap-1">
                {data.versions.map((v) => (
                  <li key={v.version}>
                    Version {v.version}: {v.source === "rotation" ? "rotated" : "typed in"}{" "}
                    {formatTime(v.created_at)}
                    {v.status !== "retired" && (
                      <Badge
                        className="ml-2"
                        variant={
                          v.status === "current" ? "green" : v.status === "failed" ? "red" : "amber"
                        }
                      >
                        {v.status}
                      </Badge>
                    )}
                  </li>
                ))}
              </ul>
            </div>
          </>
        )}
      </CardContent>
    </Card>
  );
}

const reasonLabels = {
  privileged: "Privileged access",
  orphan: "Nobody's",
  service: "Service account",
};

function Discovery({ admin, onAdd }: { admin: boolean; onAdd: (f: VaultFinding) => void }) {
  const { data, error, isPending } = useQuery({
    queryKey: ["vault-discovery"],
    queryFn: discoverVaultAccounts,
  });
  return (
    <section className="flex flex-col gap-3">
      <p className="text-sm text-slate-500">
        Accounts in your applications that hold high or critical access, belong to nobody, or belong
        to a service. Grantra finds them in what it last read from each application.
      </p>
      <Loaded
        loading={isPending}
        error={error}
        empty={
          data?.length === 0 && "Nothing found. Connect and read applications to find accounts."
        }
      >
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>Account</TableHead>
              <TableHead>Why</TableHead>
              <TableHead>Belongs to</TableHead>
              <TableHead className="relative">
                <span className="sr-only">Actions</span>
              </TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {data?.map((f) => (
              <TableRow key={f.account_id} className="align-top">
                <TableCell>
                  <div className="font-medium">{f.username || f.native_id}</div>
                  <div className="text-slate-500">{f.application_name}</div>
                </TableCell>
                <TableCell>
                  <div className="flex flex-wrap gap-1">
                    {f.reasons.map((r) => (
                      <Badge key={r} variant={r === "privileged" ? "red" : "amber"}>
                        {reasonLabels[r]}
                      </Badge>
                    ))}
                  </div>
                  {f.entitlements.length > 0 && (
                    <div className="text-slate-500">{f.entitlements.join(", ")}</div>
                  )}
                </TableCell>
                <TableCell>
                  {f.identity_id ? (
                    <Link className="underline" to={`/people/${f.identity_id}`}>
                      {f.identity_name}
                    </Link>
                  ) : (
                    <span className="text-slate-500">Nobody</span>
                  )}
                </TableCell>
                <TableCell className="text-right">
                  {f.vault_account_id ? (
                    <Badge variant="green">In the vault</Badge>
                  ) : (
                    admin && (
                      <Button size="sm" variant="outline" onClick={() => onAdd(f)}>
                        Add to vault
                      </Button>
                    )
                  )}
                </TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      </Loaded>
    </section>
  );
}
