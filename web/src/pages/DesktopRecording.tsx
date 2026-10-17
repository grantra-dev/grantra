import { useEffect, useRef, useState } from "react";
import { Alert } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { desktopRecordingURL } from "@/lib/gateway";
import { Guacamole, type GuacamoleTypes } from "@/lib/guacamole";

const clock = (ms: number) => {
  const s = Math.floor(ms / 1000);
  const h = Math.floor(s / 3600);
  const mm = String(Math.floor((s % 3600) / 60)).padStart(h ? 2 : 1, "0");
  return `${h ? `${h}:` : ""}${mm}:${String(s % 60).padStart(2, "0")}`;
};

/** Plays a desktop session's screen recording. */
export function DesktopRecording({ sessionID }: { sessionID: string }) {
  const screen = useRef<HTMLDivElement>(null);
  const player = useRef<GuacamoleTypes.SessionRecording | null>(null);
  const [problem, setProblem] = useState("");
  const [loaded, setLoaded] = useState(false);
  const [playing, setPlaying] = useState(false);
  const [position, setPosition] = useState(0);
  const [duration, setDuration] = useState(0);

  useEffect(() => {
    const el = screen.current;
    if (!el) return;
    let rec: GuacamoleTypes.SessionRecording | null = null;
    let timer: ReturnType<typeof setInterval> | undefined;
    const ctl = new AbortController();
    fetch(desktopRecordingURL(sessionID), { signal: ctl.signal })
      .then(async (r) => {
        if (!r.ok) throw new Error(`Grantra could not load the recording (${r.status}).`);
        const blob = await r.blob();
        if (blob.size === 0) throw new Error("Nothing was recorded.");
        rec = new Guacamole.SessionRecording(blob);
        player.current = rec;
        const display = rec.getDisplay();
        const view = display.getElement();
        el.appendChild(view);
        display.onresize = (w, h) => {
          if (w && h) display.scale(Math.min(1, el.clientWidth / w));
        };
        rec.onprogress = (d) => setDuration(d);
        rec.onload = () => {
          setLoaded(true);
          setDuration(rec?.getDuration() ?? 0);
        };
        rec.onerror = (m) => setProblem(m);
        rec.onplay = () => setPlaying(true);
        rec.onpause = () => setPlaying(false);
        rec.onseek = (p) => setPosition(p);
        timer = setInterval(() => rec && setPosition(rec.getPosition()), 250);
        rec.connect();
      })
      .catch((e: Error) => {
        if (e.name !== "AbortError") setProblem(e.message);
      });
    return () => {
      ctl.abort();
      clearInterval(timer);
      rec?.pause();
      rec?.abort();
      rec?.disconnect();
      el.replaceChildren();
      player.current = null;
    };
  }, [sessionID]);

  return (
    <div className="flex flex-col gap-3">
      {problem && <Alert variant="error">{problem}</Alert>}
      <div className="flex flex-wrap items-center gap-3 text-sm">
        <Button
          size="sm"
          disabled={!loaded}
          onClick={() => (playing ? player.current?.pause() : player.current?.play())}
        >
          {playing ? "Pause" : "Play"}
        </Button>
        <input
          type="range"
          aria-label="Position"
          className="min-w-40 flex-1"
          min={0}
          max={duration}
          value={Math.min(position, duration)}
          disabled={!loaded}
          onChange={(e) => player.current?.seek(Number(e.target.value))}
        />
        <span className="tabular-nums text-slate-500">
          {clock(position)} / {loaded ? clock(duration) : "loading…"}
        </span>
      </div>
      <div ref={screen} className="overflow-hidden rounded-md bg-slate-900" />
    </div>
  );
}
