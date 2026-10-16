import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import { PlatformRole, type Me } from "@/lib/api";
import { connectCommand, type SSHCommand, type SSHSession, type SSHTarget } from "@/lib/ssh";
import { ok, renderAt, serve } from "@/test-utils";

const player = vi.hoisted(() => ({ create: vi.fn(), seek: vi.fn(), dispose: vi.fn() }));
vi.mock("asciinema-player", () => ({
  create: (...args: unknown[]) => {
    player.create(...args);
    return { seek: player.seek, dispose: player.dispose };
  },
}));

const ana: Me = {
  identity_id: "ana",
  display_name: "Ana Núñez",
  platform_roles: [],
  manages_people: false,
  owns_applications: false,
};
const admin: Me = { ...ana, identity_id: "adm", platform_roles: [PlatformRole.Admin] };
const auditor: Me = { ...ana, identity_id: "aud", platform_roles: [PlatformRole.Auditor] };

const target: SSHTarget = {
  id: "t1",
  name: "prod-db",
  address: "10.0.0.5:22",
  login: "postgres",
  entitlement_id: "e1",
  entitlement_name: "Log in to prod-db as postgres",
  risk: "high",
  max_certificate_hours: 8,
  via_gateway: true,
  created_at: "2026-10-16T10:00:00Z",
  held: true,
};
const info = {
  enabled: true,
  gateway_address: "ssh.example.com:2222",
  ca_public_key: "ssh-ed25519 AAAACA grantra",
  gateway_ca_public_key: "ssh-ed25519 AAAAGW grantra",
};
const session: SSHSession = {
  id: "s1",
  target_id: "t1",
  target_name: "prod-db",
  login: "postgres",
  identity_id: "ana",
  identity_name: "Ana Núñez",
  client_address: "10.1.1.1:5555",
  gateway: "gw-1",
  kind: "shell",
  width: 80,
  height: 24,
  started_at: "2026-10-16T10:00:00Z",
  terminate_requested: false,
  recording_bytes: 100,
  recording_truncated: false,
};
const command: SSHCommand = {
  id: 1,
  session_id: "s1",
  at: "2026-10-16T10:01:00Z",
  offset_seconds: 65.4,
  command: "psql -c 'DROP TABLE orders'",
  target_name: "prod-db",
  login: "postgres",
  identity_id: "ana",
  identity_name: "Ana Núñez",
};

const body = (init?: RequestInit) => JSON.parse(init!.body as string) as unknown;

describe("Servers", () => {
  it("signs a certificate for a server you hold and says how to connect", async () => {
    let sent: unknown;
    serve({
      "GET /api/v1/me": ok(ana),
      "GET /api/v1/ssh": ok(info),
      "GET /api/v1/ssh/targets": ok({ targets: [target] }),
      "POST /api/v1/ssh/targets/t1/certificates": (init) => {
        sent = body(init);
        return {
          status: 201,
          body: {
            certificate: "ssh-ed25519-cert-v01@openssh.com AAAACERT",
            valid_before: "2026-10-16T18:00:00Z",
          },
        };
      },
    });
    renderAt("/servers");
    expect(await screen.findByText("prod-db")).toBeInTheDocument();
    expect(screen.getByText("Recorded")).toBeInTheDocument();
    expect(screen.queryByRole("tab", { name: "Sessions" })).not.toBeInTheDocument();
    expect(screen.queryByText(/Make a server trust Grantra/)).not.toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Get certificate" }));
    const sign = screen.getByRole("button", { name: "Sign certificate" });
    expect(sign).toBeDisabled();
    await userEvent.type(
      screen.getByLabelText("Your public key"),
      "ssh-ed25519 AAAAKEY ana@laptop",
    );
    await userEvent.click(sign);
    expect(await screen.findByDisplayValue(/AAAACERT/)).toBeInTheDocument();
    expect(sent).toEqual({ public_key: "ssh-ed25519 AAAAKEY ana@laptop" });
    expect(screen.getByText("ssh -p 2222 prod-db@ssh.example.com")).toBeInTheDocument();
  });

  it("tells people without SSH access where to get it", async () => {
    serve({
      "GET /api/v1/me": ok(ana),
      "GET /api/v1/ssh": ok(info),
      "GET /api/v1/ssh/targets": ok({ targets: [] }),
    });
    renderAt("/servers");
    expect(await screen.findByText(/You hold no SSH access right now/)).toBeInTheDocument();
  });

  it("lets admins add a server and shows the key servers trust", async () => {
    let sent: unknown;
    serve({
      "GET /api/v1/me": ok(admin),
      "GET /api/v1/ssh": ok(info),
      "GET /api/v1/ssh/targets": ok({ targets: [{ ...target, held: false }] }),
      "POST /api/v1/ssh/targets": (init) => {
        sent = body(init);
        return { status: 201, body: target };
      },
    });
    renderAt("/servers");
    expect(await screen.findByLabelText("Reached directly")).toHaveValue(info.ca_public_key);
    expect(screen.getByLabelText("Through the gateway (recorded)")).toHaveValue(
      info.gateway_ca_public_key,
    );
    expect(screen.queryByRole("button", { name: "Get certificate" })).not.toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Add server" }));
    await userEvent.type(screen.getByLabelText("Name"), "web-1");
    await userEvent.type(screen.getByLabelText("Address"), "10.0.0.9");
    await userEvent.type(screen.getByLabelText("Log in as"), "deploy");
    await userEvent.selectOptions(screen.getByLabelText("Risk"), "critical");
    // The form's own button; the one that opened it is gone.
    await userEvent.click(screen.getByRole("button", { name: "Add server" }));
    await waitFor(() =>
      expect(sent).toEqual({
        name: "web-1",
        login: "deploy",
        address: "10.0.0.9",
        risk: "critical",
      }),
    );
  });

  it("lists sessions for auditors, and lets only admins end one", async () => {
    let ended = false;
    const routes = {
      "GET /api/v1/ssh/sessions?limit=200": ok({ sessions: [session] }),
      "POST /api/v1/ssh/sessions/s1/terminate": () => {
        ended = true;
        return { status: 204, body: null };
      },
    };
    serve({ "GET /api/v1/me": ok(auditor), ...routes });
    const { unmount } = renderAt("/servers?tab=sessions");
    expect(await screen.findByText("Running")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Watch" })).toHaveAttribute(
      "href",
      "/servers/sessions/s1",
    );
    expect(screen.queryByRole("button", { name: "End" })).not.toBeInTheDocument();
    unmount();

    serve({ "GET /api/v1/me": ok(admin), ...routes });
    renderAt("/servers?tab=sessions");
    await userEvent.click(await screen.findByRole("button", { name: "End" }));
    await waitFor(() => expect(ended).toBe(true));
  });

  it("finds who typed a command and links to that moment", async () => {
    serve({
      "GET /api/v1/me": ok(auditor),
      "GET /api/v1/ssh/commands?q=DROP+TABLE&limit=200": ok({ commands: [command] }),
    });
    renderAt("/servers?tab=commands");
    await userEvent.type(await screen.findByLabelText("Command"), "DROP TABLE");
    await userEvent.click(screen.getByRole("button", { name: "Search" }));
    const link = await screen.findByRole("link", { name: command.command });
    expect(link).toHaveAttribute("href", "/servers/sessions/s1?t=65");
  });

  it("plays a recording and jumps to a command", async () => {
    serve({
      "GET /api/v1/me": ok(auditor),
      "GET /api/v1/ssh/sessions/s1": ok({
        ...session,
        ended_at: "2026-10-16T10:05:00Z",
        end_reason: "access_ended",
        commands: [command],
      }),
    });
    renderAt("/servers/sessions/s1?t=65");
    expect(await screen.findByText("Access ended")).toBeInTheDocument();
    await waitFor(() => expect(player.create).toHaveBeenCalled());
    const [src, , opts] = player.create.mock.calls[0] as [
      { url: string },
      unknown,
      { startAt: number },
    ];
    expect(src.url).toBe("/api/v1/ssh/sessions/s1/recording");
    expect(opts.startAt).toBe(65);
    await userEvent.click(screen.getByRole("button", { name: /DROP TABLE/ }));
    expect(player.seek).toHaveBeenCalledWith(65.4);
    expect(screen.getByText("1:05")).toBeInTheDocument();
  });
});

describe("connectCommand", () => {
  it("goes through the gateway, or straight to the server", () => {
    expect(connectCommand(target, "gw.example.com")).toBe("ssh prod-db@gw.example.com");
    expect(connectCommand({ ...target, via_gateway: false }, "gw.example.com:2222")).toBe(
      "ssh postgres@10.0.0.5",
    );
    expect(connectCommand({ ...target, via_gateway: false, address: "[fd00::5]:2200" }, "")).toBe(
      "ssh -p 2200 postgres@fd00::5",
    );
  });
});
