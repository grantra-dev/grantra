import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { Loaded } from "@/components/Loaded";
import { RiskBadge } from "@/components/RiskBadge";
import { Alert } from "@/components/ui/alert";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input, Label, Textarea } from "@/components/ui/field";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { type Me } from "@/lib/api";
import { focusRing, useFocusParam } from "@/lib/focus";
import { cn, formatTime } from "@/lib/utils";
import {
  breakGlass,
  checkinVault,
  checkoutVaultAccount,
  listVaultAccounts,
  type NewVaultAccount,
  type VaultAccount,
  type VaultLease,
} from "@/lib/vault";
import { NewAccount } from "./AccountForms";
import { AccountDetail } from "./AccountDetail";

export function Accounts({
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
  // The search box links here with ?account=<id>.
  const asked = useFocusParam("account", !!data);
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
                focused={asked === a.id}
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
  focused,
  expanded,
  onToggle,
}: {
  account: VaultAccount;
  me: Me;
  reader: boolean;
  focused: boolean;
  expanded: boolean;
  onToggle: () => void;
}) {
  const [checkingOut, setCheckingOut] = useState(false);
  const [breaking, setBreaking] = useState(false);
  const mine = a.checkouts.find((c) => c.identity_id === me.identity_id);
  const others = a.checkouts.filter((c) => c.identity_id !== me.identity_id);
  return (
    <>
      <TableRow data-focus={a.id} className={cn("align-top", focused && focusRing)}>
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
            {a.break_glass && <Badge variant="red">Break glass</Badge>}
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
          <div className="flex flex-col items-end gap-2">
            {a.held && a.version > 0 && !checkingOut && !breaking && (
              <Button size="sm" onClick={() => setCheckingOut(true)}>
                {mine ? "Show again" : "Check out"}
              </Button>
            )}
            {a.responder && a.version > 0 && !checkingOut && !breaking && (
              <Button size="sm" variant="destructive" onClick={() => setBreaking(true)}>
                Break glass
              </Button>
            )}
          </div>
        </TableCell>
      </TableRow>
      {(checkingOut || breaking) && (
        <TableRow>
          <TableCell colSpan={4}>
            <CheckOut
              account={a}
              emergency={breaking}
              onDone={() => {
                setCheckingOut(false);
                setBreaking(false);
              }}
            />
          </TableCell>
        </TableRow>
      )}
    </>
  );
}

function CheckOut({
  account,
  emergency,
  onDone,
}: {
  account: VaultAccount;
  emergency: boolean;
  onDone: () => void;
}) {
  const client = useQueryClient();
  const [reason, setReason] = useState("");
  const [lease, setLease] = useState<VaultLease | null>(null);
  const [copied, setCopied] = useState(false);
  const refresh = () => void client.invalidateQueries({ queryKey: ["vault-accounts"] });
  const checkout = useMutation({
    mutationFn: () =>
      emergency ? breakGlass(account.id, reason) : checkoutVaultAccount(account.id, reason),
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
        {(account.rotate_after_checkin || (lease.break_glass && account.can_rotate)) && (
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
      {emergency && (
        <Alert variant="warning">
          Breaking the glass opens {account.name} for up to an hour, even if someone else has it.
          Every admin and auditor is told at once, and an admin reviews it afterwards.
        </Alert>
      )}
      <Label htmlFor="vault-reason">
        {emergency ? "What is the emergency?" : `Why do you need ${account.name}?`}
      </Label>
      <Input
        id="vault-reason"
        required
        maxLength={1000}
        value={reason}
        onChange={(e) => setReason(e.target.value)}
        placeholder="INC-1234: restart the replica"
      />
      {!emergency && (
        <p className="text-xs text-slate-500">
          It stays checked out for {account.checkout_minutes} minutes. Your reason goes to the audit
          log.
        </p>
      )}
      <div className="flex gap-2">
        <Button
          size="sm"
          type="submit"
          variant={emergency ? "destructive" : "default"}
          disabled={checkout.isPending}
        >
          {emergency ? "Break glass" : "Check out"}
        </Button>
        <Button size="sm" variant="ghost" type="button" onClick={onDone}>
          Cancel
        </Button>
      </div>
      {checkout.error && <Alert variant="error">{checkout.error.message}</Alert>}
    </form>
  );
}
