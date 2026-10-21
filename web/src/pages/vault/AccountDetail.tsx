import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { Link } from "react-router";
import { Alert, ErrorAlert } from "@/components/ui/alert";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Input, Label } from "@/components/ui/field";
import { formatTime } from "@/lib/utils";
import {
  checkinVault,
  deleteVaultAccount,
  getVaultAccount,
  rotateVaultAccount,
  setVaultSecret,
  updateVaultAccount,
  type VaultAccount,
  type VaultSettings,
} from "@/lib/vault";
import { SettingsFields } from "./AccountForms";

const endedLabels = { checked_in: "Checked in", expired: "Expired", ended: "Ended by an admin" };

export function AccountDetail({
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
        <ErrorAlert error={error} />
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
                        break_glass: data.break_glass,
                        break_glass_responders: (data.break_glass_responders ?? []).map(
                          (r) => r.id,
                        ),
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
                    <SettingsFields
                      value={settings}
                      kind={data.kind}
                      known={data.break_glass_responders}
                      onChange={setSettings}
                    />
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
                    <ErrorAlert error={save.error} className="sm:col-span-2" />
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
