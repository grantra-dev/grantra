import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import type { Me } from "@/lib/api";
import type {
  CatalogApplication,
  IdentitySource,
  ProvisioningRun,
  SoDViolation,
  SyncResult,
} from "@/lib/admin";
import { ok, renderAt, serve } from "@/test-utils";

const admin: Me = {
  identity_id: "ada",
  display_name: "Ada Admin",
  platform_roles: ["grantra-admin"],
  manages_people: false,
  owns_applications: false,
};

const source: IdentitySource = {
  id: "hr",
  name: "Workday export",
  kind: "csv",
  config: { missing: "leave", max_leaver_percent: 10 },
  created_at: "2026-10-01T09:00:00Z",
};

const run = (extra: Partial<SyncResult>): SyncResult => ({
  id: "r1",
  source_id: "hr",
  actor: "identity:ada",
  dry_run: true,
  status: "planned",
  counts: { joiner: 1, leaver: 2 },
  errors: [],
  warnings: [],
  created_at: "2026-10-14T09:00:00Z",
  changes: [{ kind: "joiner", external_id: "E063", display_name: "Nuria Vidal", status: "active" }],
  unchanged: 4,
  ...extra,
});

describe("HR sources", () => {
  it("checks a file, then applies the same file", async () => {
    vi.spyOn(window, "confirm").mockReturnValue(true);
    const sent: { query: string; body: unknown }[] = [];
    const sync = (dryRun: boolean) => (init?: RequestInit) => {
      sent.push({ query: `dry_run=${dryRun}`, body: init?.body });
      return {
        status: 200,
        body: run({ dry_run: dryRun, status: dryRun ? "planned" : "applied" }),
      };
    };
    serve({
      "GET /api/v1/me": ok(admin),
      "GET /api/v1/identity-sources": ok({ sources: [source] }),
      "POST /api/v1/identity-sources/hr/runs?dry_run=true": sync(true),
      "POST /api/v1/identity-sources/hr/runs?dry_run=false": sync(false),
    });
    renderAt("/settings/hr-sources");

    const file = new File(["employee_id\nE063\n"], "workday.csv", { type: "text/csv" });
    await userEvent.upload(await screen.findByLabelText("HR export for Workday export"), file);
    expect(screen.getByRole("button", { name: "Apply" })).toBeDisabled();
    await userEvent.click(screen.getByRole("button", { name: "Check file" }));

    expect(await screen.findByText("1 joiner, 2 leavers")).toBeInTheDocument();
    expect(screen.getByText("Nuria Vidal")).toBeInTheDocument();
    expect(screen.getByText(/Nothing has changed yet/)).toBeInTheDocument();

    await userEvent.click(screen.getByRole("button", { name: "Apply" }));
    expect(await screen.findByText(/Applied\. Joiners and movers/)).toBeInTheDocument();
    expect(sent.map((s) => s.query)).toEqual(["dry_run=true", "dry_run=false"]);
    expect(sent[1]!.body).toBe(file);
  });

  it("offers to force a sync the leaver limit stopped", async () => {
    vi.spyOn(window, "confirm").mockReturnValue(true);
    let forced = false;
    serve({
      "GET /api/v1/me": ok(admin),
      "GET /api/v1/identity-sources": ok({ sources: [source] }),
      "POST /api/v1/identity-sources/hr/runs?dry_run=true": () => ({
        status: 409,
        body: run({
          status: "refused",
          errors: [{ message: "2 of 5 active people would become leavers" }],
        }),
      }),
      "POST /api/v1/identity-sources/hr/runs?dry_run=false&force=true": () => {
        forced = true;
        return { status: 200, body: run({ dry_run: false, status: "applied" }) };
      },
    });
    renderAt("/settings/hr-sources");

    await userEvent.upload(
      await screen.findByLabelText("HR export for Workday export"),
      new File(["x"], "workday.csv"),
    );
    await userEvent.click(screen.getByRole("button", { name: "Check file" }));
    expect(
      await screen.findByText("2 of 5 active people would become leavers"),
    ).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Apply anyway" }));
    await waitFor(() => expect(forced).toBe(true));
  });
});

describe("Applications", () => {
  const app: CatalogApplication = {
    id: "gh",
    name: "GitHub",
    connector: "scim",
    config: { base_url: "https://github.example/scim/v2", token: "env:GRANTRA_SECRET_GH" },
    provisioning: { mode: "off" },
    entitlement_count: 1,
    created_at: "2026-10-01T09:00:00Z",
  };

  it("plans provisioning without writing anything", async () => {
    const plan: ProvisioningRun = {
      id: "p1",
      application_id: "gh",
      kind: "provisioning",
      actor: "identity:ada",
      status: "planned",
      counts: {},
      started_at: "2026-10-14T09:00:00Z",
      actions: [
        {
          op: "add_member",
          identity_id: "pedro",
          username: "pedro",
          native_entitlement: "devs",
          status: "planned",
        },
      ],
    };
    serve({
      "GET /api/v1/me": ok(admin),
      "GET /api/v1/applications": ok({ applications: [app] }),
      "GET /api/v1/applications/gh/entitlements": ok({ entitlements: [] }),
      "POST /api/v1/applications/gh/provisioning-runs?dry_run=true": ok(plan),
      "GET /api/v1/applications/gh/runs?limit=20": ok({ runs: [] }),
    });
    renderAt("/governance/applications/gh");

    expect(await screen.findByText("env:GRANTRA_SECRET_GH")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Dry run" }));
    expect(await screen.findByText("1 change planned. Nothing was written.")).toBeInTheDocument();
    expect(screen.getByText("Add to group")).toBeInTheDocument();
  });

  it("explains a read that failed", async () => {
    serve({
      "GET /api/v1/me": ok(admin),
      "GET /api/v1/applications": ok({ applications: [app] }),
      "GET /api/v1/applications/gh/entitlements": ok({ entitlements: [] }),
      "POST /api/v1/applications/gh/aggregations": () => ({
        status: 502,
        body: { status: "failed", error: "connection refused", counts: {} },
      }),
    });
    renderAt("/governance/applications/gh");

    await userEvent.click(await screen.findByRole("button", { name: "Read accounts now" }));
    expect(
      await screen.findByText("Could not read the application: connection refused"),
    ).toBeInTheDocument();
  });
});

describe("Application authorization", () => {
  const app: CatalogApplication = {
    id: "gh",
    name: "Billing",
    connector: "manual",
    config: {},
    provisioning: { mode: "off" },
    entitlement_count: 1,
    created_at: "2026-10-01T09:00:00Z",
  };

  it("saves policies, tries a question and creates a key", async () => {
    const sent: Record<string, unknown> = {};
    serve({
      "GET /api/v1/me": ok(admin),
      "GET /api/v1/applications": ok({ applications: [app] }),
      "GET /api/v1/applications/gh/authz": ok({ policies: "", keys: [] }),
      "PUT /api/v1/applications/gh/authz/policies": (init) => {
        sent.policies = JSON.parse(init!.body as string);
        return { status: 200, body: sent.policies };
      },
      "POST /api/v1/applications/gh/authz/test": (init) => {
        sent.test = JSON.parse(init!.body as string);
        return {
          status: 200,
          body: {
            allowed: true,
            policies: ["finance approves invoices"],
            person: {
              id: "p1",
              email: "ana@example.com",
              display_name: "Ana",
              status: "active",
              attributes: { department: "Finance" },
              roles: [],
              entitlements: ["approver"],
            },
          },
        };
      },
      "POST /api/v1/applications/gh/authz/keys": () => ({
        status: 201,
        body: {
          key: { id: "k1", name: "backend", prefix: "grantra_ak_abc123" },
          secret: "grantra_ak_s3cret",
        },
      }),
    });
    renderAt("/governance/applications/gh?tab=authorization");

    await userEvent.click(await screen.findByRole("button", { name: "Start from an example" }));
    await userEvent.type(screen.getByLabelText("Person"), "ana@example.com");
    await userEvent.type(screen.getByLabelText("Action"), "approve");
    await userEvent.type(screen.getByLabelText("Resource type"), "Invoice");
    await userEvent.type(screen.getByLabelText("Resource ID"), "42");
    await userEvent.type(screen.getByLabelText("Resource attributes (JSON)"), '{{"amount": 1200}');
    await userEvent.click(screen.getByRole("button", { name: "Ask" }));
    expect(await screen.findByText(/Allowed by finance approves invoices/)).toBeInTheDocument();
    expect(sent.test).toMatchObject({
      principal: { email: "ana@example.com" },
      action: "approve",
      resource: { type: "Invoice", id: "42", attributes: { amount: 1200 } },
    });
    expect((sent.test as { policies: string }).policies).toContain("finance approves invoices");

    await userEvent.click(screen.getByRole("button", { name: "Save policies" }));
    await waitFor(() =>
      expect((sent.policies as { policies: string }).policies).toContain('Action::"approve"'),
    );

    await userEvent.type(screen.getByLabelText("Key name"), "backend");
    await userEvent.click(screen.getByRole("button", { name: "Create key" }));
    expect(await screen.findByText("grantra_ak_s3cret")).toBeInTheDocument();
  });
});

describe("Separation of duties", () => {
  it("accepts the risk of a violation with a reason and an end", async () => {
    const violation: SoDViolation = {
      id: "v1",
      policy_id: "p1",
      policy_name: "Payments",
      severity: "high",
      identity_id: "laura",
      identity_name: "Laura Gómez",
      status: "open",
      detected_at: "2026-10-14T09:00:00Z",
    };
    let sent: unknown;
    serve({
      "GET /api/v1/me": ok(admin),
      "GET /api/v1/sod-policies": ok({ policies: [] }),
      "GET /api/v1/sod-violations?status=open&limit=200": ok({ violations: [violation] }),
      "POST /api/v1/sod-violations/v1/exception": (init) => {
        sent = JSON.parse(init!.body as string);
        return { status: 200, body: { ...violation, status: "excepted" } };
      },
    });
    renderAt("/governance/separation-of-duties");

    await userEvent.click(await screen.findByRole("button", { name: "Accept the risk" }));
    await userEvent.type(
      screen.getByLabelText("Why Laura Gómez may keep both"),
      "Covers the finance lead's leave",
    );
    await userEvent.selectOptions(screen.getByLabelText("For how long"), "90");
    await userEvent.click(screen.getByRole("button", { name: "Accept" }));
    await waitFor(() =>
      expect(sent).toEqual({ reason: "Covers the finance lead's leave", days: 90 }),
    );
  });
});

describe("Grantra Login", () => {
  it("sets the sign-in policy, registers an app, federates a provider and imports a realm", async () => {
    const sent: Record<string, unknown> = {};
    serve({
      "GET /api/v1/me": ok(admin),
      "GET /api/v1/login": ok({
        connected: true,
        application: { id: "kc", name: "Grantra Login" },
        realm: { realm: "grantra", display_name: "Grantra Login", login_theme: "grantra", url: "" },
        policy: { mfa: "optional", passkeys: false },
        identity_providers: [
          {
            alias: "old",
            display_name: "Old login",
            kind: "oidc",
            enabled: true,
            link_by_email: true,
            domains: [],
            redirect_uri: "http://kc/realms/grantra/broker/old/endpoint",
          },
        ],
        apps: [
          {
            id: "c1",
            client_id: "grantra-console",
            name: "Grantra console",
            enabled: true,
            public: false,
            redirect_uris: ["http://localhost:8080/auth/callback"],
          },
        ],
      }),
      "POST /api/v1/login/identity-providers": (init) => {
        sent.idp = JSON.parse(init!.body as string);
        return { status: 201, body: {} };
      },
      "PUT /api/v1/login/identity-providers/old/domains": (init) => {
        sent.domains = JSON.parse(init!.body as string);
        return { status: 200, body: sent.domains };
      },
      "POST /api/v1/login/import?dry_run=true": () => ({
        status: 200,
        body: {
          realm: "legacy",
          users: { add: ["marta"], skip: ["ana"] },
          groups: { add: ["Finance"], skip: [] },
          roles: { add: [], skip: [] },
          apps: { add: ["intranet"], skip: [] },
          notes: ["1 service account left out: they belong to the clients that own them."],
          applied: false,
        },
      }),
      "POST /api/v1/login/import?dry_run=false": () => ({
        status: 200,
        body: {
          realm: "legacy",
          users: { add: ["marta"], skip: ["ana"] },
          groups: { add: [], skip: [] },
          roles: { add: [], skip: [] },
          apps: { add: [], skip: [] },
          notes: [],
          applied: true,
        },
      }),
      "PUT /api/v1/login/policy": (init) => {
        sent.policy = JSON.parse(init!.body as string);
        return { status: 200, body: { policy: sent.policy, sync: { added: 2, removed: 0 } } };
      },
      "POST /api/v1/login/apps": (init) => {
        sent.app = JSON.parse(init!.body as string);
        return {
          status: 201,
          body: { app: { id: "c2", client_id: "wiki", name: "Wiki" }, secret: "s3cret" },
        };
      },
    });
    renderAt("/settings/grantra-login");

    expect(await screen.findByText("grantra-console")).toBeInTheDocument();
    await userEvent.click(screen.getByLabelText(/For people with sensitive access/));
    await userEvent.click(screen.getByLabelText(/Passkeys/));
    await userEvent.click(screen.getByRole("button", { name: "Save policy" }));
    await waitFor(() => expect(sent.policy).toEqual({ mfa: "sensitive", passkeys: true }));

    await userEvent.type(screen.getByLabelText("Name"), "Wiki");
    await userEvent.type(screen.getByLabelText("Client ID"), "wiki");
    await userEvent.type(
      screen.getByLabelText("Redirect URIs, one per line"),
      "https://wiki.example.com/cb",
    );
    await userEvent.click(screen.getByRole("button", { name: "Register app" }));
    expect(await screen.findByText("s3cret")).toBeInTheDocument();
    expect(sent.app).toEqual({
      name: "Wiki",
      client_id: "wiki",
      public: false,
      redirect_uris: ["https://wiki.example.com/cb"],
    });

    // Federation.
    await userEvent.type(screen.getByLabelText("Email domains for Old login"), "acme.com, acme.io");
    await userEvent.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(sent.domains).toEqual({ domains: ["acme.com", "acme.io"] }));

    await userEvent.type(screen.getByLabelText("Button label"), "Microsoft");
    await userEvent.type(screen.getByLabelText("Alias"), "entra");
    await userEvent.type(screen.getByLabelText("Tenant ID or domain"), "acme.onmicrosoft.com");
    await userEvent.type(screen.getByLabelText("Client ID at the provider"), "abc");
    await userEvent.type(screen.getByLabelText("Client secret at the provider"), "xyz");
    await userEvent.click(screen.getByRole("button", { name: "Add provider" }));
    await waitFor(() =>
      expect(sent.idp).toMatchObject({
        kind: "entra",
        alias: "entra",
        display_name: "Microsoft",
        tenant: "acme.onmicrosoft.com",
        client_id: "abc",
        client_secret: "xyz",
        link_by_email: true,
      }),
    );

    // Import.
    vi.spyOn(window, "confirm").mockReturnValue(true);
    await userEvent.upload(
      screen.getByLabelText("Keycloak realm export"),
      new File(['{"realm": "legacy"}'], "legacy.json", { type: "application/json" }),
    );
    await userEvent.click(screen.getByRole("button", { name: "Check file" }));
    expect(await screen.findByText(/1 service account left out/)).toBeInTheDocument();
    expect(screen.getByText(/1 already there/)).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Import" }));
    expect(await screen.findByText("Imported realm legacy.")).toBeInTheDocument();
  });
});
