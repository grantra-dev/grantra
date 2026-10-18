import { useMutation, useQuery } from "@tanstack/react-query";
import { useState } from "react";
import { Link } from "react-router";
import { Loaded } from "@/components/Loaded";
import { RiskBadge } from "@/components/RiskBadge";
import { Alert } from "@/components/ui/alert";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { buttonVariants } from "@/components/ui/button-variants";
import { Input, Label, Textarea } from "@/components/ui/field";
import {
  awsExports,
  connectCommand,
  gatewayAddress,
  getGateway,
  isCloud,
  issueGatewayToken,
  kindText,
  listGatewayTargets,
  openConsole,
  usesToken,
  type ConsoleSession,
  type GatewayInfo,
  type GatewayTarget,
  type GatewayToken,
  type TokenKind,
} from "@/lib/gateway";
import { focusRing, useFocusParam } from "@/lib/focus";
import { cn, formatTime } from "@/lib/utils";
import { NewTarget, TargetSettings } from "./TargetForms";

export function Targets({ admin, reader }: { admin: boolean; reader: boolean }) {
  const info = useQuery({ queryKey: ["gateway"], queryFn: getGateway });
  const { data, error, isPending } = useQuery({
    queryKey: ["gateway-targets"],
    queryFn: listGatewayTargets,
  });
  const [adding, setAdding] = useState(false);
  const [open, setOpen] = useState<string | null>(null);
  // The search box links here with ?target=<id>.
  const asked = useFocusParam("target", !!data);
  return (
    <section className="flex flex-col gap-3">
      {info.data && !info.data.enabled && (
        <Alert variant="warning">
          The gateway is off: an operator has to set GRANTRA_VAULT_KEY and restart Grantra.
        </Alert>
      )}
      {admin && (
        <div className="flex justify-end">
          {!adding && (
            <Button variant="outline" onClick={() => setAdding(true)}>
              Add a target
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
            ? "No databases, clusters, desktops or cloud consoles yet."
            : "You hold no remote access right now. Request it under Request access, or activate it from My access.")
        }
      >
        <ul className="divide-y divide-slate-200">
          {data?.map((t) => (
            <TargetRow
              key={t.id}
              t={t}
              admin={admin}
              info={info.data}
              focused={asked === t.id}
              open={open === t.id}
              onToggle={() => setOpen(open === t.id ? null : t.id)}
            />
          ))}
        </ul>
      </Loaded>
    </section>
  );
}

/** What a target is, in a line. */
function describe(t: GatewayTarget) {
  const hours = `${t.max_token_hours} ${t.max_token_hours === 1 ? "hour" : "hours"}`;
  const as = t.vault_account_name ? ` · logs in as ${t.vault_account_name}` : "";
  switch (t.kind) {
    case "rdp":
      return `${t.address}${as} · sessions last up to ${hours}`;
    case "aws":
      return `Role ${t.address}${as} · sessions last up to ${hours}`;
    case "azure":
    case "gcp":
      return `${t.cloud_role} on ${t.address}${as} · sessions last up to ${hours}`;
    default:
      return `${t.address}${t.database ? ` · ${t.database}` : ""}${as} · tokens last up to ${hours}`;
  }
}

/**
 * Opens url in a new tab. The tab opens on the click, before the URL is
 * known, so browsers do not block it as a pop-up.
 */
function newTab() {
  const w = window.open("", "_blank");
  return {
    // With pop-ups blocked, the panel's link opens it: leaving the page
    // would lose what it shows only once.
    go: (url: string) => {
      if (!w) return;
      w.opener = null;
      w.location.href = url;
    },
    close: () => w?.close(),
  };
}

function TargetRow({
  t,
  admin,
  info,
  focused,
  open,
  onToggle,
}: {
  t: GatewayTarget;
  admin: boolean;
  info: GatewayInfo | undefined;
  focused: boolean;
  open: boolean;
  onToggle: () => void;
}) {
  const [token, setToken] = useState<GatewayToken | null>(null);
  const [opened, setOpened] = useState<ConsoleSession | null>(null);
  const issue = useMutation({ mutationFn: () => issueGatewayToken(t.id), onSuccess: setToken });
  const sign = useMutation({
    mutationFn: async (tab: ReturnType<typeof newTab>) => {
      try {
        const s = await openConsole(t.id);
        tab.go(s.url);
        return s;
      } catch (e) {
        tab.close();
        throw e;
      }
    },
    onSuccess: setOpened,
  });
  const desktopsOff = t.kind === "rdp" && info !== undefined && !info.desktops_enabled;
  const err = issue.error ?? sign.error;
  return (
    <li data-focus={t.id} className={cn("p-4", focused && focusRing)}>
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div className="min-w-0">
          <div className="flex flex-wrap items-center gap-2">
            <span className="font-medium">{t.name}</span>
            <Badge>{kindText[t.kind]}</Badge>
            <RiskBadge risk={t.risk} />
          </div>
          <div className="break-all text-sm text-slate-500">{describe(t)}</div>
          {t.notes && <p className="mt-1 text-sm text-slate-700">{t.notes}</p>}
          {t.held && desktopsOff && (
            <p className="mt-1 text-sm text-amber-700">
              Desktops are off: an operator has to set up guacd (GRANTRA_GUACD_ADDR).
            </p>
          )}
        </div>
        <div className="flex gap-2">
          {t.held && usesToken(t.kind) && !token && (
            <Button size="sm" onClick={() => issue.mutate()} disabled={issue.isPending}>
              Get token
            </Button>
          )}
          {t.held && t.kind === "rdp" && !desktopsOff && (
            <Link
              to={`/remote-access/desktops/${t.id}`}
              target="_blank"
              className={buttonVariants({ size: "sm" })}
            >
              Connect
            </Link>
          )}
          {t.held && isCloud(t.kind) && (
            <Button size="sm" onClick={() => sign.mutate(newTab())} disabled={sign.isPending}>
              Open console
            </Button>
          )}
          {admin && (
            <Button size="sm" variant="outline" onClick={onToggle}>
              {open ? "Close" : "Settings"}
            </Button>
          )}
        </div>
      </div>
      {err && (
        <Alert variant="error" className="mt-3">
          {err.message}
        </Alert>
      )}
      {token && usesToken(t.kind) && (
        <TokenPanel
          t={t}
          kind={t.kind as TokenKind}
          token={token}
          info={info}
          onClose={() => setToken(null)}
        />
      )}
      {opened && <ConsolePanel t={t} s={opened} onClose={() => setOpened(null)} />}
      {open && <TargetSettings t={t} onDone={onToggle} />}
    </li>
  );
}

function ConsolePanel({
  t,
  s,
  onClose,
}: {
  t: GatewayTarget;
  s: ConsoleSession;
  onClose: () => void;
}) {
  return (
    <div className="mt-3 flex flex-col gap-2 rounded-md border border-slate-200 bg-slate-50 p-3 text-sm">
      <p>
        The {kindText[t.kind]} console opened in a new tab. Your session lasts until{" "}
        <strong>{formatTime(s.valid_before)}</strong>, or until your access ends
        {t.kind === "aws" && " (on AWS, an ended session stays signed in until then)"}.{" "}
        <a href={s.url} target="_blank" rel="noreferrer" className="text-brand-700 underline">
          Open it again
        </a>
      </p>
      {s.credentials && (
        <>
          <p>For the AWS CLI, in a terminal (Grantra shows them only now):</p>
          <pre className="overflow-x-auto rounded bg-slate-900 p-2 text-xs text-slate-100">
            {awsExports(s.credentials)}
          </pre>
        </>
      )}
      <div>
        <Button size="sm" variant="outline" onClick={onClose}>
          Done
        </Button>
      </div>
    </div>
  );
}

/** Offers text as a file to save. */
function download(name: string, text: string) {
  const url = URL.createObjectURL(new Blob([text], { type: "text/plain" }));
  const a = document.createElement("a");
  a.href = url;
  a.download = name;
  a.click();
  URL.revokeObjectURL(url);
}

function TokenPanel({
  t,
  kind,
  token,
  info,
  onClose,
}: {
  t: GatewayTarget;
  kind: TokenKind;
  token: GatewayToken;
  info: GatewayInfo | undefined;
  onClose: () => void;
}) {
  const gateway = gatewayAddress(info, kind);
  return (
    <div className="mt-3 flex flex-col gap-2 rounded-md border border-slate-200 bg-slate-50 p-3 text-sm">
      <p>
        Valid until <strong>{formatTime(token.valid_before)}</strong>, or until your access ends.
        Grantra shows it only now.
      </p>
      {t.kind === "kubernetes" ? (
        <>
          <p>Save the kubeconfig, which holds the token, and use it with kubectl:</p>
          <Textarea
            aria-label="Kubeconfig"
            readOnly
            rows={6}
            value={token.kubeconfig ?? ""}
            className="font-mono text-xs"
            onFocus={(e) => e.currentTarget.select()}
          />
          <div>
            <Button
              size="sm"
              variant="outline"
              onClick={() => download(`grantra-${t.name}.yaml`, token.kubeconfig ?? "")}
            >
              Download kubeconfig
            </Button>
          </div>
        </>
      ) : (
        <>
          <p>
            Save the gateway&apos;s certificate as <code>grantra-gateway-ca.pem</code>, then connect
            and give the token as the password:
          </p>
          <Label htmlFor={`token-${t.id}`}>Token</Label>
          <Input
            id={`token-${t.id}`}
            readOnly
            value={token.token}
            className="font-mono text-xs"
            onFocus={(e) => e.currentTarget.select()}
          />
          <div>
            <Button
              size="sm"
              variant="outline"
              onClick={() => download("grantra-gateway-ca.pem", token.ca_certificate)}
            >
              Download certificate
            </Button>
          </div>
        </>
      )}
      <pre className="overflow-x-auto rounded bg-slate-900 p-2 text-xs text-slate-100">
        {connectCommand(t, gateway)}
      </pre>
      {gateway.startsWith("GATEWAY:") && (
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
