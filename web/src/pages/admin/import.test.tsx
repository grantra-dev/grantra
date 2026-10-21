import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import type { CatalogApplication, ConnectorRun, IdentitySource, ServiceToken } from "@/lib/admin";
import { adminMe as admin, ok, renderAt, serve } from "@/test-utils";

const source: IdentitySource = {
  id: "hr",
  name: "Workday export",
  kind: "csv",
  config: { missing: "leave", max_leaver_percent: 10, columns: { employee_id: "Worker ID" } },
  created_at: "2026-10-01T09:00:00Z",
};

const json = (init?: RequestInit) => JSON.parse(init!.body as string) as unknown;

describe("HR source columns", () => {
  it("maps columns from a sample file when creating a source", async () => {
    let sent: unknown;
    serve({
      "GET /api/v1/me": ok(admin),
      "GET /api/v1/identity-sources": ok({ sources: [] }),
      "POST /api/v1/identity-sources": (init) => {
        sent = json(init);
        return { status: 201, body: source };
      },
    });
    renderAt("/settings/hr-sources");

    await userEvent.click(await screen.findByRole("button", { name: "New HR source" }));
    const form = screen.getByRole("form", { name: "New HR source" });
    await userEvent.type(within(form).getByLabelText("Name"), "Workday export");
    await userEvent.upload(
      within(form).getByLabelText("Sample file"),
      new File(["Worker ID,Full Name,Email\nW1,Ana,ana@example.com\n"], "sample.csv"),
    );
    // Email matches on its own; the id does not until it is mapped.
    const email = within(form).getByLabelText("Work email").parentElement!;
    expect(await within(email).findByText("In the file.")).toBeInTheDocument();
    const id = within(form).getByLabelText("Employee id").parentElement!;
    expect(within(id).getByText("Not in the file.")).toBeInTheDocument();

    await userEvent.type(within(form).getByLabelText("Employee id"), "Worker ID");
    await userEvent.type(within(form).getByLabelText("Full name"), "Full Name");
    expect(within(id).getByText("In the file.")).toBeInTheDocument();
    await userEvent.click(within(form).getByRole("button", { name: "Add source" }));

    await waitFor(() =>
      expect(sent).toEqual({
        name: "Workday export",
        kind: "csv",
        config: {
          columns: { employee_id: "Worker ID", display_name: "Full Name" },
          missing: "leave",
          max_leaver_percent: 10,
        },
      }),
    );
  });

  it("edits a source's columns", async () => {
    let sent: unknown;
    serve({
      "GET /api/v1/me": ok(admin),
      "GET /api/v1/identity-sources": ok({ sources: [source] }),
      "PUT /api/v1/identity-sources/hr": (init) => {
        sent = json(init);
        return { status: 200, body: source };
      },
    });
    renderAt("/settings/hr-sources");

    expect(await screen.findByText("Columns: employee_id from “Worker ID”")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Edit" }));
    const form = screen.getByRole("form", { name: "Edit Workday export" });
    expect(within(form).getByLabelText("Employee id")).toHaveValue("Worker ID");
    await userEvent.clear(within(form).getByLabelText("Employee id"));
    await userEvent.type(within(form).getByLabelText("Department"), "Org Unit");
    await userEvent.click(within(form).getByRole("button", { name: "Save" }));

    await waitFor(() =>
      expect(sent).toEqual({
        name: "Workday export",
        config: { columns: { department: "Org Unit" }, missing: "leave", max_leaver_percent: 10 },
      }),
    );
  });
});

describe("HR systems", () => {
  it("connects BambooHR with a typed key, then checks and applies it", async () => {
    const sent: unknown[] = [];
    const bamboo: IdentitySource = {
      id: "bh",
      name: "BambooHR",
      kind: "bamboohr",
      config: { connection: { company: "acme", api_key: "secret:k1" }, every_hours: 12 },
      created_at: "2026-10-01T09:00:00Z",
    };
    const run = (dryRun: boolean) => ({
      status: 200,
      body: {
        id: "r1",
        source_id: "bh",
        actor: "ada",
        dry_run: dryRun,
        status: dryRun ? "planned" : "applied",
        counts: { joiner: 2 },
        errors: [],
        warnings: [],
        created_at: "2026-10-01T09:00:00Z",
        changes: [],
        unchanged: 0,
      },
    });
    let sources: IdentitySource[] = [];
    serve({
      "GET /api/v1/me": ok(admin),
      "GET /api/v1/identity-sources": () => ({ status: 200, body: { sources } }),
      "POST /api/v1/connector-secrets": (init) => {
        sent.push(json(init));
        return { status: 201, body: { ref: "secret:k1" } };
      },
      "POST /api/v1/identity-sources": (init) => {
        sent.push(json(init));
        sources = [bamboo];
        return { status: 201, body: bamboo };
      },
      "POST /api/v1/identity-sources/bh/pulls?dry_run=true": () => run(true),
      "POST /api/v1/identity-sources/bh/pulls?dry_run=false": () => run(false),
    });
    vi.spyOn(window, "confirm").mockReturnValue(true);
    renderAt("/settings/hr-sources");

    await userEvent.click(await screen.findByRole("button", { name: "New HR source" }));
    const form = screen.getByRole("form", { name: "New HR source" });
    await userEvent.selectOptions(within(form).getByLabelText("People come from"), "bamboohr");
    // BambooHR names its fields itself: nothing to map, no file to send.
    expect(within(form).queryByLabelText("Employee id")).not.toBeInTheDocument();
    await userEvent.type(within(form).getByLabelText("Name"), "BambooHR");
    await userEvent.type(within(form).getByLabelText("Company subdomain"), "acme");
    await userEvent.clear(within(form).getByLabelText("Read it every (hours)"));
    await userEvent.type(within(form).getByLabelText("Read it every (hours)"), "12");
    const add = within(form).getByRole("button", { name: "Add source" });
    expect(add).toBeDisabled();
    await userEvent.type(within(form).getByLabelText("API key"), "bamboo-key");
    await userEvent.click(add);

    await waitFor(() =>
      expect(sent).toEqual([
        { label: "BambooHR: API key", value: "bamboo-key" },
        {
          name: "BambooHR",
          kind: "bamboohr",
          config: {
            max_leaver_percent: 10,
            connection: { company: "acme", api_key: "secret:k1" },
            every_hours: 12,
          },
        },
      ]),
    );
    expect(await screen.findByText(/BambooHR · read every 12 h/)).toBeInTheDocument();
    expect(screen.queryByLabelText("HR export for BambooHR")).not.toBeInTheDocument();
    const apply = screen.getByRole("button", { name: "Apply" });
    expect(apply).toBeDisabled();
    await userEvent.click(screen.getByRole("button", { name: "Check BambooHR now" }));
    expect(await screen.findByText(/Nothing has changed yet/)).toBeInTheDocument();
    await userEvent.click(apply);
    expect(await screen.findByText(/Applied\./)).toBeInTheDocument();
  });
});

describe("Account files", () => {
  const app = (extra: Partial<CatalogApplication>): CatalogApplication => ({
    id: "erp",
    name: "Legacy ERP",
    connector: "manual",
    config: {},
    provisioning: { mode: "off" },
    read_every_hours: 1,
    entitlement_count: 0,
    created_at: "2026-10-01T09:00:00Z",
    ...extra,
  });
  const run = (extra: Partial<ConnectorRun>): ConnectorRun => ({
    id: "r1",
    application_id: "erp",
    kind: "aggregation",
    actor: "identity:ada",
    status: "planned",
    counts: { accounts_new: 2 },
    started_at: "2026-10-14T09:00:00Z",
    ...extra,
  });

  it("sets an application up to read files", async () => {
    let sent: unknown;
    serve({
      "GET /api/v1/me": ok(admin),
      "GET /api/v1/applications": ok({ applications: [app({})] }),
      "GET /api/v1/applications/erp/entitlements": ok({ entitlements: [] }),
      "PUT /api/v1/applications/erp/connector": (init) => {
        sent = json(init);
        return { status: 204, body: null };
      },
    });
    renderAt("/governance/applications/erp");

    await userEvent.click(await screen.findByRole("button", { name: "Load accounts from a file" }));
    const form = screen.getByRole("form", { name: "Accounts from a file" });
    await userEvent.type(within(form).getByLabelText("User name"), "Login");
    await userEvent.type(within(form).getByLabelText("Entitlements"), "Roles");
    await userEvent.clear(within(form).getByLabelText("Between several entitlements in one cell"));
    await userEvent.type(
      within(form).getByLabelText("Between several entitlements in one cell"),
      "|",
    );
    await userEvent.click(within(form).getByRole("button", { name: "Save" }));

    await waitFor(() =>
      expect(sent).toEqual({
        kind: "csv",
        config: { columns: { username: "Login", entitlements: "Roles" }, separator: "|" },
      }),
    );
  });

  it("checks a file, then imports it", async () => {
    vi.spyOn(window, "confirm").mockReturnValue(true);
    const queries: string[] = [];
    const upload = (dryRun: boolean) => () => {
      queries.push(`dry_run=${dryRun}`);
      return { status: 200, body: run({ status: dryRun ? "planned" : "succeeded" }) };
    };
    serve({
      "GET /api/v1/me": ok(admin),
      "GET /api/v1/applications": ok({
        applications: [app({ connector: "csv", config: { separator: ";" } })],
      }),
      "GET /api/v1/applications/erp/entitlements": ok({ entitlements: [] }),
      "POST /api/v1/applications/erp/account-imports?dry_run=true": upload(true),
      "POST /api/v1/applications/erp/account-imports?dry_run=false": upload(false),
    });
    renderAt("/governance/applications/erp");

    expect(await screen.findByText(/cannot write to this application/)).toBeInTheDocument();
    await userEvent.upload(
      screen.getByLabelText("Account file for Legacy ERP"),
      new File(["username\nana\n"], "erp.csv"),
    );
    expect(screen.getByRole("button", { name: "Import" })).toBeDisabled();
    await userEvent.click(screen.getByRole("button", { name: "Check file" }));
    expect(await screen.findByText(/Nothing has changed yet/)).toBeInTheDocument();
    expect(screen.getByText("2 new accounts")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Import" }));
    await waitFor(() => expect(queries).toEqual(["dry_run=true", "dry_run=false"]));
  });

  it("shows why a file was refused", async () => {
    serve({
      "GET /api/v1/me": ok(admin),
      "GET /api/v1/applications": ok({ applications: [app({ connector: "csv" })] }),
      "GET /api/v1/applications/erp/entitlements": ok({ entitlements: [] }),
      "POST /api/v1/applications/erp/account-imports?dry_run=true": () => ({
        status: 422,
        body: run({ status: "failed", counts: {}, error: 'line 2: unknown status "maybe"' }),
      }),
    });
    renderAt("/governance/applications/erp");

    await userEvent.upload(
      await screen.findByLabelText("Account file for Legacy ERP"),
      new File(["x"], "erp.csv"),
    );
    await userEvent.click(screen.getByRole("button", { name: "Check file" }));
    expect(
      await screen.findByText('The file was not imported: line 2: unknown status "maybe"'),
    ).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Import" })).toBeDisabled();
  });
});

describe("API tokens", () => {
  const token: ServiceToken = {
    id: "t1",
    name: "Nightly HR",
    prefix: "grantra_st_abc123",
    scopes: ["identity_source.sync:hr", "application.import_accounts"],
    created_by: "identity:ada",
    creator_name: "Ada",
    created_at: "2026-10-14T09:00:00Z",
    expires_at: "2099-10-14T09:00:00Z",
  };

  it("creates a token for one source and shows its secret once", async () => {
    let sent: unknown;
    serve({
      "GET /api/v1/me": ok(admin),
      "GET /api/v1/service-tokens": ok({ tokens: [] }),
      "GET /api/v1/identity-sources": ok({ sources: [source] }),
      "GET /api/v1/applications": ok({ applications: [] }),
      "POST /api/v1/service-tokens": (init) => {
        sent = json(init);
        return { status: 201, body: { token, secret: "grantra_st_s3cret" } };
      },
    });
    renderAt("/settings/api-tokens");

    const form = await screen.findByRole("form", { name: "New token" });
    await userEvent.type(within(form).getByLabelText("Name"), "Nightly HR");
    await userEvent.selectOptions(within(form).getByLabelText("Expires after"), "90");
    await userEvent.selectOptions(
      within(form).getByLabelText("Permission 1 applies to"),
      await within(form).findByRole("option", { name: "Workday export" }),
    );
    await userEvent.click(within(form).getByRole("button", { name: "Add another permission" }));
    await userEvent.selectOptions(
      within(form).getByLabelText("Permission 2"),
      "application.import_accounts",
    );
    await userEvent.click(within(form).getByRole("button", { name: "Create token" }));

    expect(await screen.findByText("grantra_st_s3cret")).toBeInTheDocument();
    expect(
      screen.getByText(/\/api\/v1\/identity-sources\/hr\/runs\?dry_run=true/),
    ).toBeInTheDocument();
    expect(sent).toEqual({
      name: "Nightly HR",
      scopes: ["identity_source.sync:hr", "application.import_accounts"],
      expires_in_days: 90,
    });
  });

  it("lists tokens in words and revokes one", async () => {
    vi.spyOn(window, "confirm").mockReturnValue(true);
    let revoked = false;
    serve({
      "GET /api/v1/me": ok(admin),
      "GET /api/v1/service-tokens": ok({ tokens: [token] }),
      "GET /api/v1/identity-sources": ok({ sources: [source] }),
      "GET /api/v1/applications": ok({ applications: [] }),
      "DELETE /api/v1/service-tokens/t1": () => {
        revoked = true;
        return { status: 204, body: null };
      },
    });
    renderAt("/settings/api-tokens");

    expect(await screen.findByText("Feed an HR source: Workday export")).toBeInTheDocument();
    expect(screen.getByText("Load an application's account file: any")).toBeInTheDocument();
    expect(screen.getByText("Created by Ada")).toBeInTheDocument();
    expect(screen.queryByText(/Creator left/)).not.toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Revoke" }));
    await waitFor(() => expect(revoked).toBe(true));
  });

  it("flags working tokens whose creator left", async () => {
    serve({
      "GET /api/v1/me": ok(admin),
      "GET /api/v1/service-tokens": ok({
        tokens: [
          { ...token, creator_left: true },
          {
            ...token,
            id: "t2",
            name: "Old job",
            creator_left: true,
            revoked_at: "2026-10-15T09:00:00Z",
          },
        ],
      }),
      "GET /api/v1/identity-sources": ok({ sources: [source] }),
      "GET /api/v1/applications": ok({ applications: [] }),
    });
    renderAt("/settings/api-tokens");

    expect(await screen.findByText("Creator left: check who looks after it")).toBeInTheDocument();
    expect(screen.getAllByText(/Creator left/)).toHaveLength(1);
  });
});
