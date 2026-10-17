import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";
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

describe("Databases & clusters", () => {
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
    renderAt("/databases");
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
    renderAt("/databases");
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
    renderAt("/databases");
    await userEvent.click(await screen.findByRole("button", { name: "Add database or cluster" }));
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
        address: "10.0.0.7",
        database: "orders",
        tls_mode: "verify",
        ca_certificate: "",
        vault_account_id: "v1",
        risk: "high",
      }),
    );
  });

  it("shows auditors sessions and their logs", async () => {
    serve({
      "GET /api/v1/me": ok(auditor),
      "GET /api/v1/gateway/sessions?limit=200": ok({ sessions: [session] }),
      "GET /api/v1/gateway/sessions/s1": ok({ ...session, log: [event] }),
    });
    renderAt("/databases?tab=sessions");
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
    renderAt("/databases?tab=queries");
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
