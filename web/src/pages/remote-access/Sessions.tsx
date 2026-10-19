import { Link } from "react-router";
import { RecordedSearch, SessionBadge, SessionList } from "@/components/Sessions";
import {
  isCloud,
  kindText,
  listGatewaySessions,
  searchGatewayEvents,
  terminateGatewaySession,
  type GatewaySession,
} from "@/lib/gateway";
import { formatTime } from "@/lib/utils";

/** Bytes, in a unit people read. */
function sizeText(n: number) {
  if (n < 1024) return `${n} B`;
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(0)} KB`;
  if (n < 1024 * 1024 * 1024) return `${(n / 1024 / 1024).toFixed(1)} MB`;
  return `${(n / 1024 / 1024 / 1024).toFixed(1)} GB`;
}

export function Sessions({ admin }: { admin: boolean }) {
  return (
    <SessionList
      name="gateway-sessions"
      list={listGatewaySessions}
      terminate={terminateGatewaySession}
      admin={admin}
      endable={(s) => s.kind !== "aws"}
      columns={[
        [
          "Target",
          (s) => (
            <>
              {s.target_name}
              <div className="text-xs text-slate-500">
                {kindText[s.kind]}
                {s.account && ` as ${s.account}`}
                {s.database && ` on ${s.database}`}
              </div>
            </>
          ),
        ],
        [
          "Started",
          (s) => (
            <>
              {formatTime(s.started_at)}
              {!s.ended_at && s.valid_before && (
                <div className="text-xs text-slate-500">ends by {formatTime(s.valid_before)}</div>
              )}
            </>
          ),
        ],
        [
          "Logged",
          (s) =>
            s.kind === "rdp" ? sizeText(s.recording_bytes) : isCloud(s.kind) ? "—" : s.events,
        ],
      ]}
      status={(s) => <GatewaySessionStatus s={s} />}
      link={(s) =>
        !isCloud(s.kind) && (
          <Link to={`/remote-access/sessions/${s.id}`} className="text-sm text-brand-700 underline">
            {s.kind === "rdp" ? "Recording" : "Log"}
          </Link>
        )
      }
    />
  );
}

export function GatewaySessionStatus({ s }: { s: GatewaySession }) {
  return (
    <SessionBadge s={s} full={s.kind === "rdp" ? "Recording limit reached" : "Log limit reached"} />
  );
}

export function Queries() {
  return (
    <RecordedSearch
      name="gateway-events"
      search={searchGatewayEvents}
      label="Query"
      placeholder="DELETE FROM"
      note={
        <>
          SQL statements as the database got them, and cluster requests (such as{" "}
          <code>DELETE /api/v1/namespaces/shop/pods/web</code>). What goes on inside kubectl exec is
          not logged.
        </>
      }
      empty={(asked) => `Nothing matches “${asked}”.`}
      columns={[
        ["Target", (ev) => ev.target_name],
        [
          "Query or request",
          (ev) => (
            <>
              <Link
                to={`/remote-access/sessions/${ev.session_id}`}
                className="break-all font-mono text-xs text-brand-700 underline"
              >
                {ev.text}
              </Link>
              {ev.status !== undefined && (
                <span className="ml-2 text-xs text-slate-500">{ev.status}</span>
              )}
            </>
          ),
        ],
      ]}
    />
  );
}
