import { useInfiniteQuery } from "@tanstack/react-query";
import { Link, useParams } from "react-router";
import { Loaded } from "@/components/Loaded";
import { PageHeader } from "@/components/PageHeader";
import { Alert } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { getGatewaySession, kindText } from "@/lib/gateway";
import { formatTime } from "@/lib/utils";
import { DesktopRecording } from "@/pages/DesktopRecording";
import { GatewaySessionStatus } from "@/pages/RemoteAccess";

/**
 * One gateway session: its queries or cluster requests, or a desktop's
 * screen recording.
 */
export function GatewaySessionLog() {
  const { id = "" } = useParams();
  const { data, error, isPending, fetchNextPage, hasNextPage, isFetchingNextPage } =
    useInfiniteQuery({
      queryKey: ["gateway-session", id],
      queryFn: ({ pageParam }) => getGatewaySession(id, pageParam),
      initialPageParam: 0,
      getNextPageParam: (last) => (last.log.length === 500 ? last.log[499]?.id : undefined),
      // Opening the log is in the audit log: once per visit is enough.
      staleTime: Infinity,
      refetchOnWindowFocus: false,
    });
  const s = data?.pages[0];
  const log = data?.pages.flatMap((p) => p.log) ?? [];
  return (
    <>
      <PageHeader
        title={s?.kind === "rdp" ? "Desktop recording" : "Session log"}
        description="Opening a session's log or recording is recorded in the audit log."
      />
      <p className="mb-4 text-sm">
        <Link to="/remote-access?tab=sessions" className="text-brand-700 underline">
          All sessions
        </Link>
      </p>
      <Loaded loading={isPending} error={error}>
        {s && (
          <div className="flex flex-col gap-4">
            <div className="flex flex-wrap items-center gap-2 text-sm">
              <strong>{s.identity_name}</strong> on {s.target_name} ({kindText[s.kind]})
              {s.account && <> as {s.account}</>}
              {s.database && <> in {s.database}</>}, from {s.client_address} ·{" "}
              {formatTime(s.started_at)}
              {s.ended_at && <> to {formatTime(s.ended_at)}</>}
              <GatewaySessionStatus s={s} />
            </div>
            {s.error && <Alert variant="error">{s.error}</Alert>}
            {s.kind === "rdp" ? (
              <DesktopRecording sessionID={s.id} />
            ) : (
              <Card>
                <CardHeader>
                  <CardTitle>
                    {s.kind === "kubernetes" ? "Requests" : "Queries"} ({s.events})
                  </CardTitle>
                </CardHeader>
                <CardContent>
                  {log.length === 0 ? (
                    <p className="text-sm text-slate-500">Nothing logged.</p>
                  ) : (
                    <ol className="flex flex-col gap-2 font-mono text-xs">
                      {log.map((ev) => (
                        <li key={ev.id} className="flex gap-3">
                          <span className="shrink-0 text-slate-500">{formatTime(ev.at)}</span>
                          <span className="whitespace-pre-wrap break-all">{ev.text}</span>
                          {ev.status !== undefined && (
                            <span className="shrink-0 text-slate-500">{ev.status}</span>
                          )}
                        </li>
                      ))}
                    </ol>
                  )}
                  {hasNextPage && (
                    <Button
                      className="mt-3"
                      size="sm"
                      variant="outline"
                      onClick={() => void fetchNextPage()}
                      disabled={isFetchingNextPage}
                    >
                      Show more
                    </Button>
                  )}
                </CardContent>
              </Card>
            )}
          </div>
        )}
      </Loaded>
    </>
  );
}
