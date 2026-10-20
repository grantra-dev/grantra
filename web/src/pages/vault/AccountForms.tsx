import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { PersonSearch } from "@/components/PersonSearch";
import { Alert } from "@/components/ui/alert";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Input, Label, Select, Textarea } from "@/components/ui/field";
import {
  createVaultAccount,
  type NewVaultAccount,
  type Responder,
  type VaultSettings,
} from "@/lib/vault";

const emptySettings: VaultSettings = {
  address: "",
  notes: "",
  checkout_minutes: 60,
  exclusive: false,
  rotate_every_days: 0,
  rotate_after_checkin: false,
  break_glass: false,
  break_glass_responders: [],
};

export function SettingsFields({
  value,
  onChange,
  kind,
  known = [],
}: {
  value: VaultSettings;
  onChange: (v: VaultSettings) => void;
  kind: string;
  known?: Responder[];
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
        <label className="flex items-center gap-2 text-sm">
          <input
            type="checkbox"
            checked={value.break_glass}
            onChange={(e) =>
              onChange({
                ...value,
                break_glass: e.target.checked,
                break_glass_responders: e.target.checked ? value.break_glass_responders : [],
              })
            }
          />
          Break glass: responders can open it in an emergency without holding access
        </label>
      </div>
      {value.break_glass && (
        <Responders
          ids={value.break_glass_responders}
          known={known}
          onChange={(ids) => onChange({ ...value, break_glass_responders: ids })}
        />
      )}
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

/** Who may break the glass, picked by searching people. */
function Responders({
  ids,
  known,
  onChange,
}: {
  ids: string[];
  known: Responder[];
  onChange: (ids: string[]) => void;
}) {
  const [names, setNames] = useState(() => new Map(known.map((r) => [r.id, r.name])));
  return (
    <div className="flex flex-col gap-2 sm:col-span-2">
      <Label htmlFor="vault-responders">Responders</Label>
      <p className="text-xs text-slate-500">
        {ids.length === 0
          ? "Nobody listed: the Grantra admins respond."
          : "Only these people can break the glass."}
      </p>
      {ids.length > 0 && (
        <ul className="flex flex-wrap gap-2">
          {ids.map((id) => (
            <li key={id}>
              <Badge>
                {names.get(id) ?? id}
                <button
                  type="button"
                  className="ml-1"
                  aria-label={`Remove ${names.get(id) ?? id}`}
                  onClick={() => onChange(ids.filter((x) => x !== id))}
                >
                  ×
                </button>
              </Badge>
            </li>
          ))}
        </ul>
      )}
      <PersonSearch
        id="vault-responders"
        placeholder="Search people to add"
        action="Add"
        skip={ids}
        onPick={(p) => {
          setNames(new Map(names).set(p.id, p.display_name));
          onChange([...ids, p.id]);
        }}
      />
    </div>
  );
}

export function NewAccount({
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
