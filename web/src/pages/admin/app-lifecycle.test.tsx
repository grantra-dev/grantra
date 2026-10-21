import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { Identity } from "@/lib/api";
import type { AppAccount, CatalogApplication, Entitlement } from "@/lib/admin";
import { adminMe as admin, ok, renderAt, serve } from "@/test-utils";

const crm: CatalogApplication = {
  id: "crm",
  name: "CRM",
  connector: "csv",
  config: {},
  provisioning: { mode: "on" },
  read_every_hours: 1,
  entitlement_count: 1,
  created_at: "2026-10-01T09:00:00Z",
};

const pedro = { id: "pedro", display_name: "Pedro Ruiz" } as Identity;
const findPedro = {
  "GET /api/v1/identities?limit=50&q=pe&status=active&kind=human": ok({ identities: [pedro] }),
};

/** Records each request body sent with method and path. */
function recorder() {
  const sent: Record<string, unknown> = {};
  const echo =
    (key: string, reply: (body: never) => unknown = (b) => b) =>
    (init?: RequestInit) => {
      sent[key] = init?.body ? JSON.parse(init.body as string) : null;
      return { status: 200, body: reply(sent[key] as never) };
    };
  return { sent, echo };
}

afterEach(() => vi.restoreAllMocks());

describe("Application page", () => {
  it("sets the owner, description and read schedule", async () => {
    const { sent, echo } = recorder();
    serve({
      "GET /api/v1/me": ok(admin),
      "GET /api/v1/applications": ok({ applications: [crm] }),
      "GET /api/v1/applications/crm/entitlements": ok({ entitlements: [] }),
      ...findPedro,
      "PUT /api/v1/applications/crm": echo("update", (b: object) => ({ ...crm, ...b })),
    });
    renderAt("/governance/applications/crm");

    expect(await screen.findByText(/Nobody yet: requests that need the owner/)).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Edit" }));
    await userEvent.type(screen.getByLabelText("Owner"), "pe");
    await userEvent.click(await screen.findByRole("button", { name: "Pick Pedro Ruiz" }));
    await userEvent.type(screen.getByLabelText("Description"), "Sales pipeline");
    await userEvent.selectOptions(screen.getByLabelText("Read its accounts"), "24");
    await userEvent.click(screen.getByRole("button", { name: "Save" }));

    await waitFor(() =>
      expect(sent.update).toEqual({
        name: "CRM",
        description: "Sales pipeline",
        owner_id: "pedro",
        read_every_hours: 24,
      }),
    );
  });

  it("retires an application after asking", async () => {
    const { sent, echo } = recorder();
    serve({
      "GET /api/v1/me": ok(admin),
      "GET /api/v1/applications": ok({ applications: [crm] }),
      "GET /api/v1/applications/crm/entitlements": ok({ entitlements: [] }),
      "PUT /api/v1/applications/crm/retired": echo("retire", () => ({
        ...crm,
        retired_at: "2026-10-21T10:00:00Z",
      })),
    });
    const confirm = vi.spyOn(window, "confirm").mockReturnValue(true);
    renderAt("/governance/applications/crm");

    await userEvent.click(await screen.findByRole("button", { name: "Retire" }));
    expect(confirm).toHaveBeenCalledWith(expect.stringContaining("provisioning turns off"));
    await waitFor(() => expect(sent.retire).toEqual({ retired: true }));
  });

  it("shows why an application with history cannot be deleted", async () => {
    serve({
      "GET /api/v1/me": ok(admin),
      "GET /api/v1/applications": ok({ applications: [crm] }),
      "GET /api/v1/applications/crm/entitlements": ok({ entitlements: [] }),
      "DELETE /api/v1/applications/crm": () => ({
        status: 409,
        body: { error: "CRM has accounts; retire it instead to keep its history" },
      }),
    });
    vi.spyOn(window, "confirm").mockReturnValue(true);
    renderAt("/governance/applications/crm");

    await userEvent.click(await screen.findByRole("button", { name: "Delete" }));
    expect(await screen.findByText(/retire it instead/)).toBeInTheDocument();
  });

  it("changes the risk of an entitlement the application names", async () => {
    const ent: Entitlement = {
      id: "e1",
      application_id: "crm",
      application_name: "CRM",
      native_id: "admins",
      name: "Admins",
      risk: "low",
      jit_only: false,
      max_activation_hours: 4,
      activation_approval: false,
      created_at: "2026-10-01T09:00:00Z",
    };
    const { sent, echo } = recorder();
    serve({
      "GET /api/v1/me": ok(admin),
      "GET /api/v1/applications": ok({ applications: [crm] }),
      "GET /api/v1/applications/crm/entitlements": ok({ entitlements: [ent] }),
      "PUT /api/v1/entitlements/e1": echo("ent", (b: object) => ({ ...ent, ...b })),
    });
    renderAt("/governance/applications/crm");

    await userEvent.click(await screen.findByRole("button", { name: "Edit Admins" }));
    // A connected application names it, and lists it: no rename, no delete.
    expect(screen.getByLabelText("Name", { selector: "#ent-e1-name" })).toHaveAttribute("readonly");
    expect(screen.queryByRole("button", { name: "Delete Admins" })).not.toBeInTheDocument();
    await userEvent.selectOptions(
      screen.getByLabelText("Risk", { selector: "#ent-e1-risk" }),
      "critical",
    );
    await userEvent.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() =>
      expect(sent.ent).toEqual({ name: "Admins", description: "", risk: "critical" }),
    );
  });

  it("links an orphan account to its person", async () => {
    const orphan: AppAccount = {
      id: "a1",
      application_id: "crm",
      native_id: "svc-pr",
      status: "active",
      entitlements: [],
    };
    const { sent, echo } = recorder();
    serve({
      "GET /api/v1/me": ok(admin),
      "GET /api/v1/applications": ok({ applications: [crm] }),
      "GET /api/v1/applications/crm/accounts?limit=100": ok({ accounts: [orphan] }),
      ...findPedro,
      "PUT /api/v1/accounts/a1/identity": echo("link", () => ({
        ...orphan,
        identity_id: "pedro",
        identity_name: "Pedro Ruiz",
        correlated_by: "manual",
      })),
    });
    renderAt("/governance/applications/crm?tab=accounts");

    const row = (await screen.findByText("svc-pr")).closest("tr")!;
    await userEvent.click(
      within(row).getByRole("button", { name: "Change who svc-pr belongs to" }),
    );
    await userEvent.type(within(row).getByRole("searchbox"), "pe");
    await userEvent.click(await within(row).findByRole("button", { name: "Link to Pedro Ruiz" }));
    await waitFor(() => expect(sent.link).toEqual({ identity_id: "pedro" }));
  });
});

describe("New application", () => {
  it("asks for an owner", async () => {
    const { sent, echo } = recorder();
    serve({
      "GET /api/v1/me": ok(admin),
      "GET /api/v1/applications": ok({ applications: [] }),
      ...findPedro,
      "POST /api/v1/applications": echo("create", (b: object) => ({ ...crm, ...b })),
    });
    renderAt("/governance/applications");

    await userEvent.type(await screen.findByLabelText("New application"), "Payroll");
    await userEvent.type(screen.getByLabelText("Owner"), "pe");
    await userEvent.click(await screen.findByRole("button", { name: "Pick Pedro Ruiz" }));
    await userEvent.click(screen.getByRole("button", { name: "Add application" }));
    await waitFor(() => expect(sent.create).toEqual({ name: "Payroll", owner_id: "pedro" }));
  });
});
