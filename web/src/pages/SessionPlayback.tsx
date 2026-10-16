import { useQuery } from "@tanstack/react-query";
import { useEffect, useRef } from "react";
import { Link, useParams, useSearchParams } from "react-router";
import { Loaded } from "@/components/Loaded";
import { PageHeader } from "@/components/PageHeader";
import { Alert } from "@/components/ui/alert";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { getSSHSession, recordingURL } from "@/lib/ssh";
import { formatTime } from "@/lib/utils";
import { SessionStatus } from "@/pages/Servers";
import type { Player } from "asciinema-player";
import "asciinema-player/dist/bundle/asciinema-player.css";

/** Plays a recorded SSH session, with the commands typed in it. */
export function SessionPlayback() {
  const { id = "" } = useParams();
  const [params] = useSearchParams();
  const start = Number(params.get("t") ?? 0) || 0;
  const { data, error, isPending } = useQuery({
    queryKey: ["ssh-session", id],
    queryFn: () => getSSHSession(id),
  });
  const box = useRef<HTMLDivElement>(null);
  const player = useRef<Player | null>(null);
  // The player starts once the session loads, and not again when it is
  // refetched: that would restart playback and audit another view.
  const loaded = data !== undefined;

  useEffect(() => {
    const el = box.current;
    if (!el || !loaded) return;
    let disposed = false;
    // Loaded on demand: most people never open a recording.
    void import("asciinema-player").then(({ create }) => {
      if (disposed) return;
      player.current = create(
        { url: recordingURL(id), fetchOpts: { credentials: "same-origin" } },
        el,
        {
          startAt: start,
          fit: "width",
          idleTimeLimit: 2,
          terminalFontSize: "small",
        },
      );
    });
    return () => {
      disposed = true;
      player.current?.dispose();
      player.current = null;
    };
  }, [loaded, id, start]);

  return (
    <>
      <PageHeader
        title="Session recording"
        description="Watching a recording is recorded in the audit log."
      />
      <p className="mb-4 text-sm">
        <Link to="/servers?tab=sessions" className="text-brand-700 underline">
          All sessions
        </Link>
      </p>
      <Loaded loading={isPending} error={error}>
        {data && (
          <div className="flex flex-col gap-4 p-4">
            <div className="flex flex-wrap items-center gap-2 text-sm">
              <strong>{data.identity_name}</strong> on {data.target_name} as {data.login}, from{" "}
              {data.client_address} · {formatTime(data.started_at)}
              {data.ended_at && <> to {formatTime(data.ended_at)}</>}
              <SessionStatus s={data} />
            </div>
            {data.recording_truncated && (
              <Alert variant="warning">
                This session outgrew the recording limit: the recording stops part way.
              </Alert>
            )}
            {data.error && <Alert variant="error">{data.error}</Alert>}
            <div ref={box} data-testid="player" />
          </div>
        )}
      </Loaded>
      {data && data.commands.length > 0 && (
        <Card className="mt-4">
          <CardHeader>
            <CardTitle>Commands typed</CardTitle>
          </CardHeader>
          <CardContent>
            <ol className="flex flex-col gap-1 font-mono text-xs">
              {data.commands.map((c) => (
                <li key={c.id}>
                  <button
                    type="button"
                    className="text-left text-brand-700 hover:underline"
                    onClick={() => void player.current?.seek(c.offset_seconds)}
                  >
                    <span className="text-slate-500">{formatClock(c.offset_seconds)}</span>{" "}
                    {c.command}
                  </button>
                </li>
              ))}
            </ol>
          </CardContent>
        </Card>
      )}
    </>
  );
}

function formatClock(seconds: number) {
  const s = Math.floor(seconds);
  return `${Math.floor(s / 60)}:${String(s % 60).padStart(2, "0")}`;
}
