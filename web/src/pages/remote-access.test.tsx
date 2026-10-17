import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { PlatformRole, type Me } from "@/lib/api";
import {
  connectCommand,
  type GatewayEvent,
  type GatewaySession,
  type GatewayTarget,
} from "@/lib/gateway";
import { ok, renderAt, serve } from "@/test-utils";

const ana: Me = {
  identity_id: "ana",
  display_name: "Ana Núñez",
  platform_roles: [],
  manages_people: false,
  owns_applications: false,
};
const admin: Me = { ...ana, identity_id: "adm", platform_roles: [PlatformRole.Admin] };
const auditor: Me = { ...ana, identity_id: "aud", platform_roles: [PlatformRole.Auditor] };

const db: GatewayTarget = {
  id: "t1",
  name: "orders-db",
  kind: "postgres",
  address: "10.0.0.7:5432",
  database: "orders",
  tls_mode: "verify",
  kube_groups: [],
  entitlement_id: "e1",
  entitlement_name: "Connect to orders-db",
  risk: "high",
  max_token_hours: 8,
  created_at: "2026-10-17T10:00:00Z",
  updated_at: "2026-10-17T10:00:00Z",
  held: true,
};
const cluster: GatewayTarget = {
  ...db,
  id: "t2",
  name: "prod",
  kind: "kubernetes",
  address: "https://10.0.0.1:6443",
  database: undefined,
  kube_groups: ["grantra:prod"],
};
const info = {
  enabled: true,
  kubernetes_address: "gw.example.com:8443",
  postgres_address: "gw.example.com:15432",
  mysql_address: "",
  ca_certificate: "-----BEGIN CERTIFICATE-----\nCA\n-----END CERTIFICATE-----\n",
  desktops_enabled: true,
  consoles_enabled: true,
};
const desktop: GatewayTarget = {
  ...db,
  id: "t3",
  name: "jump-box",
  kind: "rdp",
  address: "10.0.0.9:3389",
  database: undefined,
  clipboard: "paste",
  vault_account_name: "CORP\\admin",
};
const aws: GatewayTarget = {
  ...db,
  id: "t4",
  name: "prod-aws",
  kind: "aws",
  address: "arn:aws:iam::123456789012:role/ReadOnly",
  database: undefined,
  max_token_hours: 1,
};
const session: GatewaySession = {
  id: "s1",
  target_id: "t1",
  target_name: "orders-db",
  kind: "postgres",
  identity_id: "ana",
  identity_name: "Ana Núñez",
  client_address: "10.1.1.1:5555",
  gateway: "gw-1",
  database: "orders",
  account: "orders_owner",
  started_at: "2026-10-17T10:00:00Z",
  terminate_requested: false,
  events: 1,
  recording_bytes: 0,
};
const event: GatewayEvent = {
  id: 7,
  session_id: "s1",
  at: "2026-10-17T10:01:00Z",
  kind: "query",
  text: "DELETE FROM orders WHERE id = 1",
  target_name: "orders-db",
  identity_id: "ana",
  identity_name: "Ana Núñez",
};

const body = (init?: RequestInit) => JSON.parse(init!.body as string) as unknown;

describe("Remote access", () => {
  afterEach(() => vi.unstubAllGlobals());

  it("gives a token for a database you hold, with the command to connect", async () => {
    serve({
      "GET /api/v1/me": ok(ana),
      "GET /api/v1/gateway": ok(info),
      "GET /api/v1/gateway/targets": ok({ targets: [db, { ...cluster, held: false }] }),
      "POST /api/v1/gateway/targets/t1/tokens": () => ({
        status: 201,
        body: {
          token: "grt_secret",
          valid_before: "2026-10-17T18:00:00Z",
          ca_certificate: info.ca_certificate,
        },
      }),
    });
    renderAt("/remote-access");
    expect(await screen.findByText("orders-db")).toBeInTheDocument();
    expect(screen.queryByRole("tab", { name: "Sessions" })).not.toBeInTheDocument();
    // Only the target held gets a token button.
    expect(screen.getAllByRole("button", { name: "Get token" })).toHaveLength(1);
    await userEvent.click(screen.getByRole("button", { name: "Get token" }));
    expect(await screen.findByDisplayValue("grt_secret")).toBeInTheDocument();
    expect(
      screen.getByText(
        'psql "host=gw.example.com port=15432 user=orders-db sslmode=verify-full sslrootcert=grantra-gateway-ca.pem"',
      ),
    ).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Download certificate" })).toBeInTheDocument();
  });

  it("gives a kubeconfig for a cluster", async () => {
    serve({
      "GET /api/v1/me": ok(ana),
      "GET /api/v1/gateway": ok(info),
      "GET /api/v1/gateway/targets": ok({ targets: [cluster] }),
      "POST /api/v1/gateway/targets/t2/tokens": () => ({
        status: 201,
        body: {
          token: "grt_k",
          kind: "kubernetes",
          valid_before: "2026-10-17T18:00:00Z",
          kubeconfig: "apiVersion: v1\nkind: Config\n",
          ca_certificate: info.ca_certificate,
        },
      }),
    });
    renderAt("/remote-access");
    await userEvent.click(await screen.findByRole("button", { name: "Get token" }));
    expect(await screen.findByLabelText("Kubeconfig")).toHaveValue(
      "apiVersion: v1\nkind: Config\n",
    );
    expect(screen.getByText("kubectl --kubeconfig grantra-prod.yaml get pods")).toBeInTheDocument();
  });

  it("lets admins add a database with a vault account", async () => {
    let sent: unknown;
    serve({
      "GET /api/v1/me": ok(admin),
      "GET /api/v1/gateway": ok(info),
      "GET /api/v1/gateway/targets": ok({ targets: [] }),
      "GET /api/v1/vault/accounts": ok({
        enabled: true,
        accounts: [
          { id: "v1", name: "orders owner", username: "orders_owner", kind: "password" },
          { id: "v2", name: "deploy key", username: "deploy", kind: "ssh_key" },
        ],
      }),
      "POST /api/v1/gateway/targets": (init) => {
        sent = body(init);
        return { status: 201, body: db };
      },
    });
    renderAt("/remote-access");
    await userEvent.click(await screen.findByRole("button", { name: "Add a target" }));
    await userEvent.type(screen.getByLabelText("Name"), "orders-db");
    await userEvent.type(screen.getByLabelText("Address"), "10.0.0.7");
    await userEvent.type(screen.getByLabelText("Default database"), "orders");
    const account = screen.getByLabelText("Logs in with");
    await waitFor(() => expect(account).toHaveTextContent("orders owner"));
    // SSH keys are no use to a database.
    expect(account).not.toHaveTextContent("deploy key");
    await userEvent.selectOptions(account, "v1");
    await userEvent.click(screen.getByRole("button", { name: "Add" }));
    await waitFor(() =>
      expect(sent).toEqual({
        name: "orders-db",
        kind: "postgres",
        risk: "high",
        address: "10.0.0.7",
        tls_mode: "verify",
        vault_account_id: "v1",
        max_token_hours: 8,
        ca_certificate: "",
        database: "orders",
      }),
    );
  });

  it("opens a desktop you hold in its own tab, without a token", async () => {
    serve({
      "GET /api/v1/me": ok(ana),
      "GET /api/v1/gateway": ok(info),
      "GET /api/v1/gateway/targets": ok({ targets: [desktop] }),
    });
    renderAt("/remote-access");
    const connect = await screen.findByRole("link", { name: "Connect" });
    expect(connect).toHaveAttribute("href", "/remote-access/desktops/t3");
    expect(connect).toHaveAttribute("target", "_blank");
    expect(screen.queryByRole("button", { name: "Get token" })).not.toBeInTheDocument();
    expect(screen.getByText(/logs in as CORP\\admin/)).toBeInTheDocument();
  });

  it("says so when desktops are off", async () => {
    serve({
      "GET /api/v1/me": ok(ana),
      "GET /api/v1/gateway": ok({ ...info, desktops_enabled: false }),
      "GET /api/v1/gateway/targets": ok({ targets: [desktop] }),
    });
    renderAt("/remote-access");
    expect(await screen.findByText(/Desktops are off/)).toBeInTheDocument();
    expect(screen.queryByRole("link", { name: "Connect" })).not.toBeInTheDocument();
  });

  it("signs in to the AWS console in a new tab and shows CLI credentials", async () => {
    const tab = { opener: {}, location: { href: "" }, close: vi.fn() };
    vi.stubGlobal(
      "open",
      vi.fn(() => tab),
    );
    serve({
      "GET /api/v1/me": ok(ana),
      "GET /api/v1/gateway": ok(info),
      "GET /api/v1/gateway/targets": ok({ targets: [aws] }),
      "POST /api/v1/gateway/targets/t4/console": () => ({
        status: 201,
        body: {
          session_id: "s9",
          url: "https://signin.aws.amazon.com/federation?Action=login",
          valid_before: "2026-10-17T11:00:00Z",
          credentials: {
            access_key_id: "ASIAEXAMPLE",
            secret_access_key: "secret",
            session_token: "token",
            expiration: "2026-10-17T11:00:00Z",
          },
        },
      }),
    });
    renderAt("/remote-access");
    await userEvent.click(await screen.findByRole("button", { name: "Open console" }));
    await waitFor(() =>
      expect(tab.location.href).toBe("https://signin.aws.amazon.com/federation?Action=login"),
    );
    // The new tab cannot reach back into Grantra.
    expect(tab.opener).toBeNull();
    expect(screen.getByText(/export AWS_ACCESS_KEY_ID=ASIAEXAMPLE/)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Get token" })).not.toBeInTheDocument();
  });

  it("closes the blank tab when the console does not open", async () => {
    const tab = { opener: {}, location: { href: "" }, close: vi.fn() };
    vi.stubGlobal(
      "open",
      vi.fn(() => tab),
    );
    serve({
      "GET /api/v1/me": ok(ana),
      "GET /api/v1/gateway": ok(info),
      "GET /api/v1/gateway/targets": ok({ targets: [aws] }),
      "POST /api/v1/gateway/targets/t4/console": () => ({
        status: 409,
        body: { error: "AWS refused: the role does not trust the IAM user" },
      }),
    });
    renderAt("/remote-access");
    await userEvent.click(await screen.findByRole("button", { name: "Open console" }));
    expect(await screen.findByText(/the role does not trust the IAM user/)).toBeInTheDocument();
    expect(tab.close).toHaveBeenCalled();
    expect(tab.location.href).toBe("");
  });

  it("lets admins add a Google Cloud project, sending only what it takes", async () => {
    let sent: unknown;
    serve({
      "GET /api/v1/me": ok(admin),
      "GET /api/v1/gateway": ok(info),
      "GET /api/v1/gateway/targets": ok({ targets: [] }),
      "GET /api/v1/vault/accounts": ok({ enabled: true, accounts: [] }),
      "POST /api/v1/gateway/targets": (init) => {
        sent = body(init);
        return { status: 201, body: db };
      },
    });
    renderAt("/remote-access");
    await userEvent.click(await screen.findByRole("button", { name: "Add a target" }));
    await userEvent.selectOptions(screen.getByLabelText("Kind"), "gcp");
    // Clouds always verify TLS, and have no database.
    expect(screen.queryByLabelText(/TLS to the/)).not.toBeInTheDocument();
    expect(screen.queryByLabelText("Default database")).not.toBeInTheDocument();
    expect(screen.getByLabelText(/Grants roles as/)).toHaveTextContent(
      "Grantra's own Google Cloud service account",
    );
    await userEvent.type(screen.getByLabelText("Name"), "shop");
    await userEvent.type(screen.getByLabelText("Project ID"), "shop-prod-123");
    await userEvent.type(screen.getByLabelText("Role people get"), "roles/compute.viewer");
    await userEvent.click(screen.getByRole("button", { name: "Add" }));
    await waitFor(() =>
      expect(sent).toEqual({
        name: "shop",
        kind: "gcp",
        risk: "high",
        address: "shop-prod-123",
        tls_mode: "verify",
        vault_account_id: "",
        max_token_hours: 8,
        cloud_role: "roles/compute.viewer",
      }),
    );
  });

  it("caps AWS sessions at 12 hours, defaulting to 1", async () => {
    serve({
      "GET /api/v1/me": ok(admin),
      "GET /api/v1/gateway": ok(info),
      "GET /api/v1/gateway/targets": ok({ targets: [] }),
      "GET /api/v1/vault/accounts": ok({ enabled: true, accounts: [] }),
    });
    renderAt("/remote-access");
    await userEvent.click(await screen.findByRole("button", { name: "Add a target" }));
    await userEvent.selectOptions(screen.getByLabelText("Kind"), "aws");
    const hours = screen.getByLabelText("Sessions last up to (hours)");
    expect(hours).toHaveValue(1);
    expect(hours).toHaveAttribute("max", "12");
    expect(screen.getByLabelText("IAM role ARN")).toBeInTheDocument();
  });

  it("lists desktop recordings, and keeps admins from ending AWS sessions", async () => {
    serve({
      "GET /api/v1/me": ok(admin),
      "GET /api/v1/gateway/sessions?limit=200": ok({
        sessions: [
          { ...session, id: "s2", kind: "rdp", target_name: "jump-box", recording_bytes: 3 << 20 },
          {
            ...session,
            id: "s3",
            kind: "aws",
            target_name: "prod-aws",
            valid_before: "2026-10-17T11:00:00Z",
          },
        ],
      }),
    });
    renderAt("/remote-access?tab=sessions");
    expect(await screen.findByRole("link", { name: "Recording" })).toHaveAttribute(
      "href",
      "/remote-access/sessions/s2",
    );
    expect(screen.getByText("3.0 MB")).toBeInTheDocument();
    // Only the desktop can be ended: AWS has no way to end a console session.
    expect(screen.getAllByRole("button", { name: "End" })).toHaveLength(1);
    expect(screen.queryByRole("link", { name: "Log" })).not.toBeInTheDocument();
    expect(screen.getByText(/ends by/)).toBeInTheDocument();
  });

  it("sends the old address to the new one", async () => {
    serve({
      "GET /api/v1/me": ok(ana),
      "GET /api/v1/gateway": ok(info),
      "GET /api/v1/gateway/targets": ok({ targets: [db] }),
    });
    renderAt("/databases");
    expect(await screen.findByRole("heading", { name: "Remote access" })).toBeInTheDocument();
  });

  it("shows auditors sessions and their logs", async () => {
    serve({
      "GET /api/v1/me": ok(auditor),
      "GET /api/v1/gateway/sessions?limit=200": ok({ sessions: [session] }),
      "GET /api/v1/gateway/sessions/s1": ok({ ...session, log: [event] }),
    });
    renderAt("/remote-access?tab=sessions");
    expect(await screen.findByText("Running")).toBeInTheDocument();
    // Auditors read; only admins end sessions.
    expect(screen.queryByRole("button", { name: "End" })).not.toBeInTheDocument();
    await userEvent.click(screen.getByRole("link", { name: "Log" }));
    expect(await screen.findByText("DELETE FROM orders WHERE id = 1")).toBeInTheDocument();
    expect(screen.getByText(/as orders_owner/)).toBeInTheDocument();
  });

  it("finds queries", async () => {
    serve({
      "GET /api/v1/me": ok(auditor),
      "GET /api/v1/gateway/events?q=delete&limit=200": ok({ events: [event] }),
    });
    renderAt("/remote-access?tab=queries");
    await userEvent.type(await screen.findByLabelText("Query"), "delete");
    await userEvent.click(screen.getByRole("button", { name: "Search" }));
    expect(
      await screen.findByRole("link", { name: "DELETE FROM orders WHERE id = 1" }),
    ).toBeInTheDocument();
  });
});

describe("connectCommand", () => {
  it("builds the client commands", () => {
    expect(connectCommand({ name: "billing", kind: "mysql" }, "gw:13306")).toBe(
      "mysql -h gw -P 13306 -u billing -p --ssl-mode=VERIFY_IDENTITY --ssl-ca=grantra-gateway-ca.pem",
    );
  });
});
