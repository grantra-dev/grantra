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
    renderAt("/admin");

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
    renderAt("/admin");

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
    renderAt("/admin/applications/gh");

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
    renderAt("/admin/applications/gh");

    await userEvent.click(await screen.findByRole("button", { name: "Read accounts now" }));
    expect(
      await screen.findByText("Could not read the application: connection refused"),
    ).toBeInTheDocument();
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
    renderAt("/admin?tab=sod");

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
  it("sets the sign-in policy and registers an app", async () => {
    const sent: Record<string, unknown> = {};
    serve({
      "GET /api/v1/me": ok(admin),
      "GET /api/v1/login": ok({
        connected: true,
        application: { id: "kc", name: "Grantra Login" },
        realm: { realm: "grantra", display_name: "Grantra Login", login_theme: "grantra", url: "" },
        policy: { mfa: "optional", passkeys: false },
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
    renderAt("/admin?tab=login");

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
  });
});
