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
    // Streamed from Grantra as it plays. (This client's Blob source is
    // broken: it reads the Blob before keeping it.)
    const rec = new Guacamole.SessionRecording(
      new Guacamole.StaticHTTPTunnel(desktopRecordingURL(sessionID)),
    );
    player.current = rec;
    const display = rec.getDisplay();
    el.appendChild(display.getElement());
    display.onresize = (w) => {
      if (w) display.scale(Math.min(1, el.clientWidth / w));
    };
    // The client says nothing when the network fails before any answer.
    let heard = false;
    rec.onprogress = (d) => {
      heard = true;
      setDuration(d);
    };
    rec.onload = () => {
      heard = true;
      setLoaded(true);
      setDuration(rec.getDuration());
    };
    rec.onerror = (m) => {
      heard = true;
      setProblem(m || "Grantra could not load the recording.");
    };
    rec.onplay = () => setPlaying(true);
    rec.onpause = () => setPlaying(false);
    rec.onseek = (p) => setPosition(p);
    const timer = setInterval(() => setPosition(rec.getPosition()), 250);
    const silent = setTimeout(() => {
      if (!heard) setProblem("Grantra could not load the recording.");
    }, 30000);
    rec.connect();
    return () => {
      clearInterval(timer);
      clearTimeout(silent);
      rec.pause();
      rec.abort();
      rec.disconnect();
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
