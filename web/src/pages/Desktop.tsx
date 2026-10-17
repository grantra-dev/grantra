import { useQuery } from "@tanstack/react-query";
import { useEffect, useRef, useState } from "react";
import { useParams } from "react-router";
import { Button } from "@/components/ui/button";
import { Textarea } from "@/components/ui/field";
import { desktopTunnelURL, listGatewayTargets } from "@/lib/gateway";
import { Guacamole, type GuacamoleTypes } from "@/lib/guacamole";

/** Guacamole's client states. */
const Connected = 3;
const Disconnected = 5;

/**
 * A desktop through the gateway, in its own tab: Grantra logs in with the
 * vault account, and records the screen.
 */
export function Desktop() {
  const { id = "" } = useParams();
  const targets = useQuery({ queryKey: ["gateway-targets"], queryFn: listGatewayTargets });
  const t = targets.data?.find((x) => x.id === id);
  const screen = useRef<HTMLDivElement>(null);
  const client = useRef<GuacamoleTypes.Client | null>(null);
  const [state, setState] = useState<"connecting" | "connected" | "ended">("connecting");
  const [problem, setProblem] = useState("");
  const [clipOpen, setClipOpen] = useState(false);
  const [clip, setClip] = useState("");

  useEffect(() => {
    document.title = t ? `${t.name} · Grantra` : "Desktop · Grantra";
  }, [t]);

  useEffect(() => {
    const el = screen.current;
    if (!el || !id) return;
    const c = new Guacamole.Client(new Guacamole.WebSocketTunnel(desktopTunnelURL(id)));
    client.current = c;
    const display = c.getDisplay();
    const view = display.getElement();
    el.appendChild(view);

    // Fit the desktop to the window, drawn at the screen's own pixels.
    const ratio = window.devicePixelRatio || 1;
    const fit = () => {
      const w = display.getWidth();
      const h = display.getHeight();
      if (w && h) display.scale(Math.min(el.clientWidth / w, el.clientHeight / h));
    };
    display.onresize = fit;

    c.onstatechange = (s) => {
      if (s === Connected) setState("connected");
      if (s === Disconnected) setState("ended");
    };
    c.onerror = (status) => {
      setProblem(status.message || "The desktop could not connect.");
      setState("ended");
    };
    c.onclipboard = (stream, mimetype) => {
      if (!/^text\//.test(mimetype)) return;
      const reader = new Guacamole.StringReader(stream);
      let text = "";
      reader.ontext = (part) => (text += part);
      reader.onend = () => setClip(text);
    };

    const mouse = new Guacamole.Mouse(view);
    mouse.onEach(["mousedown", "mousemove", "mouseup"], (e) => {
      c.sendMouseState((e as GuacamoleTypes.Mouse.Event).state, true);
    });
    const keyboard = new Guacamole.Keyboard(el);
    keyboard.onkeydown = (keysym) => c.sendKeyEvent(1, keysym);
    keyboard.onkeyup = (keysym) => c.sendKeyEvent(0, keysym);
    // Keys held when focus leaves (Alt+Tab) would stay down on the desktop.
    const release = () => keyboard.reset();
    window.addEventListener("blur", release);
    el.addEventListener("blur", release);
    el.focus();

    const size = () => ({
      width: Math.round(el.clientWidth * ratio),
      height: Math.round(el.clientHeight * ratio),
    });
    let resizing: ReturnType<typeof setTimeout> | undefined;
    const onResize = () => {
      clearTimeout(resizing);
      resizing = setTimeout(() => {
        const s = size();
        c.sendSize(s.width, s.height);
        fit();
      }, 300);
    };
    window.addEventListener("resize", onResize);

    const s = size();
    c.connect(
      new URLSearchParams({
        width: String(s.width),
        height: String(s.height),
        dpi: String(Math.round(96 * ratio)),
        timezone: Intl.DateTimeFormat().resolvedOptions().timeZone,
      }).toString(),
    );
    return () => {
      window.removeEventListener("resize", onResize);
      window.removeEventListener("blur", release);
      el.removeEventListener("blur", release);
      clearTimeout(resizing);
      keyboard.reset();
      keyboard.onkeydown = null;
      keyboard.onkeyup = null;
      c.disconnect();
      view.remove();
      client.current = null;
    };
  }, [id]);

  const sendClip = () => {
    const c = client.current;
    if (!c) return;
    const w = new Guacamole.StringWriter(c.createClipboardStream("text/plain"));
    w.sendText(clip);
    w.sendEnd();
    screen.current?.focus();
  };

  const clipboard = t?.clipboard ?? "paste";
  return (
    <div className="flex h-screen flex-col bg-slate-900 text-slate-100">
      <header className="flex flex-wrap items-center gap-3 px-3 py-1.5 text-sm">
        <span className="font-medium">{t?.name ?? "Desktop"}</span>
        <span className="text-slate-400">
          {state === "connecting"
            ? "Connecting…"
            : state === "connected"
              ? "Connected · the screen is recorded"
              : "Disconnected"}
        </span>
        <div className="ml-auto flex gap-2">
          {clipboard !== "none" && state === "connected" && (
            <Button size="sm" variant="outline" onClick={() => setClipOpen(!clipOpen)}>
              Clipboard
            </Button>
          )}
          {state !== "ended" && (
            <Button size="sm" variant="outline" onClick={() => client.current?.disconnect()}>
              Disconnect
            </Button>
          )}
        </div>
      </header>
      {clipOpen && clipboard !== "none" && (
        <div className="flex flex-col gap-2 border-y border-slate-700 bg-slate-800 p-3 text-sm">
          <label htmlFor="desktop-clipboard">
            {clipboard === "both"
              ? "Text copied on the desktop shows here; paste text here to send it."
              : "Paste text here to send it to the desktop's clipboard."}
          </label>
          <Textarea
            id="desktop-clipboard"
            rows={3}
            value={clip}
            onChange={(e) => setClip(e.target.value)}
            className="font-mono text-xs text-slate-900"
          />
          <div>
            <Button size="sm" onClick={sendClip}>
              Send to the desktop
            </Button>
          </div>
        </div>
      )}
      {state === "ended" && (
        <div role="alert" className="px-3 py-2 text-sm text-amber-300">
          {problem || "The session ended."} You can close this tab.
        </div>
      )}
      <div
        ref={screen}
        tabIndex={0}
        aria-label="Remote desktop"
        className="relative min-h-0 flex-1 overflow-hidden outline-none"
        onMouseDown={() => screen.current?.focus()}
      />
    </div>
  );
}
