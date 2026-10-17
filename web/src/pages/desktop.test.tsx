import { act, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { Me } from "@/lib/api";
import type { GatewayTarget } from "@/lib/gateway";
import { ok, renderAt, serve } from "@/test-utils";

// A stand-in for Guacamole's client, which needs a canvas and a WebSocket.
const g = vi.hoisted(() => {
  const state = {
    tunnelURL: "",
    connected: "",
    disconnected: 0,
    resets: 0,
    clipboard: [] as string[],
    client: null as null | {
      onstatechange: ((s: number) => void) | null;
      onerror: ((s: { code: number; message: string }) => void) | null;
    },
  };
  class Display {
    onresize = null;
    getElement() {
      return document.createElement("div");
    }
    getWidth() {
      return 0;
    }
    getHeight() {
      return 0;
    }
    scale() {}
  }
  class Client {
    onstatechange = null;
    onerror = null;
    onclipboard = null;
    display = new Display();
    constructor() {
      state.client = this;
    }
    getDisplay() {
      return this.display;
    }
    connect(data: string) {
      state.connected = data;
    }
    disconnect() {
      state.disconnected++;
    }
    sendSize() {}
    sendMouseState() {}
    sendKeyEvent() {}
    createClipboardStream() {
      return {};
    }
  }
  class StringWriter {
    sendText(t: string) {
      state.clipboard.push(t);
    }
    sendEnd() {}
  }
  const Guacamole = {
    Client,
    WebSocketTunnel: class {
      constructor(url: string) {
        state.tunnelURL = url;
      }
    },
    Mouse: class {
      onEach() {}
    },
    Keyboard: class {
      onkeydown = null;
      onkeyup = null;
      reset() {
        state.resets++;
      }
    },
    StringWriter,
    StringReader: class {},
  };
  return { state, Guacamole };
});
vi.mock("@/lib/guacamole", () => ({ Guacamole: g.Guacamole }));

const ana: Me = {
  identity_id: "ana",
  display_name: "Ana Núñez",
  platform_roles: [],
  manages_people: false,
  owns_applications: false,
};
const desktop = {
  id: "t3",
  name: "jump-box",
  kind: "rdp",
  address: "10.0.0.9:3389",
  tls_mode: "verify",
  kube_groups: [],
  clipboard: "paste",
  held: true,
} as unknown as GatewayTarget;

describe("Desktop", () => {
  afterEach(() => vi.unstubAllGlobals());

  it("connects through the tunnel with the window's size and time zone", async () => {
    serve({
      "GET /api/v1/me": ok(ana),
      "GET /api/v1/gateway/targets": ok({ targets: [desktop] }),
    });
    const { unmount } = renderAt("/remote-access/desktops/t3");
    expect(await screen.findByText("jump-box")).toBeInTheDocument();
    expect(g.state.tunnelURL).toBe("/api/v1/gateway/targets/t3/rdp");
    const q = new URLSearchParams(g.state.connected);
    expect([...q.keys()]).toEqual(["width", "height", "dpi", "timezone"]);
    expect(q.get("timezone")).toBe(Intl.DateTimeFormat().resolvedOptions().timeZone);
    expect(screen.getByText("Connecting…")).toBeInTheDocument();
    // Guacamole's "connected" state.
    act(() => g.state.client!.onstatechange!(3));
    expect(screen.getByText(/the screen is recorded/)).toBeInTheDocument();

    // Keys held when the tab loses focus are let go.
    const resets = g.state.resets;
    act(() => window.dispatchEvent(new Event("blur")));
    expect(g.state.resets).toBe(resets + 1);

    // Text pasted in Grantra goes to the desktop's clipboard.
    await userEvent.click(screen.getByRole("button", { name: "Clipboard" }));
    await userEvent.type(screen.getByLabelText(/Paste text here/), "hola");
    await userEvent.click(screen.getByRole("button", { name: "Send to the desktop" }));
    expect(g.state.clipboard).toEqual(["hola"]);

    const before = g.state.disconnected;
    unmount();
    expect(g.state.disconnected).toBe(before + 1);
  });

  it("says why the desktop ended", async () => {
    serve({
      "GET /api/v1/me": ok(ana),
      "GET /api/v1/gateway/targets": ok({ targets: [{ ...desktop, clipboard: "none" }] }),
    });
    renderAt("/remote-access/desktops/t3");
    expect(await screen.findByText("jump-box")).toBeInTheDocument();
    act(() => g.state.client!.onstatechange!(3));
    expect(screen.queryByRole("button", { name: "Clipboard" })).not.toBeInTheDocument();
    act(() => g.state.client!.onerror!({ code: 521, message: "Your access ended." }));
    await waitFor(() => expect(screen.getByRole("alert")).toHaveTextContent("Your access ended."));
    expect(screen.queryByRole("button", { name: "Disconnect" })).not.toBeInTheDocument();
  });
});
