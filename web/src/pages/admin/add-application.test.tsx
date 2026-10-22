import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import type { Identity } from "@/lib/api";
import type { AccessRule, BusinessRole, CatalogApplication, Entitlement } from "@/lib/admin";
import { adminMe as admin, ok, renderAt, serve, type Handler } from "@/test-utils";

const pedro = { id: "pedro", display_name: "Pedro Ruiz" } as Identity;
const findPedro = {
  "GET /api/v1/identities?limit=50&q=pe&status=active&kind=human": ok({ identities: [pedro] }),
};

const payroll: CatalogApplication = {
  id: "pay",
  name: "Payroll",
  connector: "manual",
  config: {},
  provisioning: { mode: "off" },
  owner_id: "pedro",
  owner_name: "Pedro Ruiz",
  read_every_hours: 1,
  entitlement_count: 0,
  created_at: "2026-10-21T09:00:00Z",
};

const clerk: Entitlement = {
  id: "e1",
  application_id: "pay",
  application_name: "Payroll",
  native_id: "clerk",
  name: "Clerk",
  risk: "low",
  jit_only: false,
  max_activation_hours: 4,
  activation_approval: false,
  created_at: "2026-10-21T09:00:00Z",
};

/** Records the body of each request sent, by "METHOD path". */
function recording(routes: Record<string, Handler>) {
  const sent: Record<string, unknown> = {};
  const wrapped = Object.fromEntries(
    Object.entries(routes).map(([key, h]) => [
      key,
      (init?: RequestInit) => {
        sent[key] = init?.body ? JSON.parse(init.body as string) : null;
        return h(init);
      },
    ]),
  );
  return { sent, routes: wrapped };
}

describe("Add application", () => {
  it("adds an application managed by hand, from its owner to going live", async () => {
    let apps: CatalogApplication[] = [];
    const { sent, routes } = recording({
      "GET /api/v1/me": ok(admin),
      "GET /api/v1/applications": () => ({ status: 200, body: { applications: apps } }),
      ...findPedro,
      "POST /api/v1/applications": () => {
        apps = [payroll];
        return { status: 201, body: payroll };
      },
      "PUT /api/v1/applications/pay": () => ({ status: 200, body: payroll }),
      "GET /api/v1/applications/pay/entitlements": ok({ entitlements: [clerk] }),
      "GET /api/v1/roles": ok({ roles: [] }),
      "GET /api/v1/access-rules/attributes": ok({ attributes: [] }),
      "POST /api/v1/access-rules/preview": ok({ matches: 0, sample: [] }),
      "GET /api/v1/applications/pay/runs?limit=20": ok({ runs: [] }),
    });
    serve(routes);
    renderAt("/governance/applications/new");

    await userEvent.type(await screen.findByLabelText("Name"), "Payroll");
    await userEvent.type(screen.getByLabelText("Owner"), "pe");
    await userEvent.click(await screen.findByRole("button", { name: "Pick Pedro Ruiz" }));
    await userEvent.type(screen.getByLabelText("Description"), "Salaries");
    await userEvent.click(screen.getByRole("button", { name: "Next" }));
    await waitFor(() =>
      expect(sent["POST /api/v1/applications"]).toEqual({ name: "Payroll", owner_id: "pedro" }),
    );
    expect(sent["PUT /api/v1/applications/pay"]).toMatchObject({ description: "Salaries" });

    // No connector: straight to its entitlements.
    await userEvent.click(await screen.findByRole("button", { name: /By hand/ }));
    expect(await screen.findByText("Clerk")).toBeInTheDocument();
    expect(screen.getByRole("listitem", { current: "step" })).toHaveTextContent("Entitlements");
    await userEvent.click(screen.getByRole("button", { name: "Next" }));

    // Birthright access is optional.
    expect(await screen.findByLabelText("What it gives")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Cancel" }));
    expect(await screen.findByText(/Needs a connector first/)).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Finish" }));
    expect(await screen.findByRole("heading", { name: "Payroll" })).toBeInTheDocument();
  });
});

describe("Roles and rules", () => {
  const routes = {
    "GET /api/v1/me": ok(admin),
    "GET /api/v1/applications": ok({ applications: [payroll] }),
    "GET /api/v1/applications/pay/entitlements": ok({ entitlements: [clerk] }),
    "GET /api/v1/roles": ok({ roles: [] as BusinessRole[] }),
    "GET /api/v1/access-rules": ok({ rules: [] as AccessRule[] }),
    "GET /api/v1/access-rules/attributes": ok({
      attributes: [{ name: "department", values: ["Finance", "Sales"] }],
    }),
  };

  it("adds a birthright rule, showing who it matches as it is written", async () => {
    const { sent, routes: recorded } = recording({
      ...routes,
      "POST /api/v1/access-rules/preview": (init) => {
        const { conditions } = JSON.parse(init!.body as string) as {
          conditions: Record<string, string[]>;
        };
        const finance = conditions.department?.includes("Finance");
        return {
          status: 200,
          body: finance
            ? { matches: 1, sample: [{ id: "ana", display_name: "Ana", status: "active" }] }
            : { matches: 9, sample: [] },
        };
      },
      "POST /api/v1/access-rules": ok({ id: "r1", changes: { added: 1, removed: 0 } }),
    });
    serve(recorded);
    renderAt("/governance/roles");

    await userEvent.click(await screen.findByRole("button", { name: "Add rule" }));
    await userEvent.type(screen.getByLabelText("Name"), "Finance gets payroll");
    await userEvent.type(screen.getByLabelText("Values 1"), "Finance");
    expect(await screen.findByText("Matches 1 person now: Ana")).toBeInTheDocument();
    await userEvent.selectOptions(screen.getByLabelText("What it gives"), "entitlement:e1");
    await userEvent.click(screen.getByRole("button", { name: "Add rule" }));

    await waitFor(() =>
      expect(sent["POST /api/v1/access-rules"]).toEqual({
        name: "Finance gets payroll",
        description: "",
        conditions: { department: ["Finance"] },
        enabled: true,
        role_ids: [],
        entitlement_ids: ["e1"],
      }),
    );
    expect(await screen.findByText(/1 person got access and 0 lost it/)).toBeInTheDocument();
  });

  it("adds a business role with its owner and entitlements", async () => {
    const { sent, routes: recorded } = recording({
      ...routes,
      ...findPedro,
      "POST /api/v1/roles": ok({ id: "role1" }),
    });
    serve(recorded);
    renderAt("/governance/roles");

    await userEvent.click(await screen.findByRole("button", { name: "Add role" }));
    await userEvent.type(screen.getByLabelText("Name"), "Payroll clerk");
    await userEvent.type(screen.getByLabelText("Owner"), "pe");
    await userEvent.click(await screen.findByRole("button", { name: "Pick Pedro Ruiz" }));
    await userEvent.selectOptions(
      await screen.findByLabelText("Entitlements it bundles"),
      "entitlement:e1",
    );
    // Roles bundle entitlements, never other roles.
    expect(screen.queryByRole("group", { name: "Business roles" })).not.toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Add role" }));
    await waitFor(() =>
      expect(sent["POST /api/v1/roles"]).toEqual({
        name: "Payroll clerk",
        description: "",
        owner_id: "pedro",
        entitlement_ids: ["e1"],
      }),
    );
  });

  it("asks before deleting a rule", async () => {
    const rule: AccessRule = {
      id: "r1",
      name: "Finance gets payroll",
      conditions: { department: ["Finance"] },
      enabled: true,
      roles: [],
      entitlements: [{ id: "e1", name: "Clerk", application: "Payroll" }],
      holders: 3,
      created_at: "2026-10-21T09:00:00Z",
      updated_at: "2026-10-21T09:00:00Z",
    };
    const { sent, routes: recorded } = recording({
      ...routes,
      "GET /api/v1/access-rules": ok({ rules: [rule] }),
      "DELETE /api/v1/access-rules/r1": ok({ changes: { added: 0, removed: 3 } }),
    });
    serve(recorded);
    const confirm = vi.spyOn(window, "confirm").mockReturnValue(true);
    renderAt("/governance/roles");

    await userEvent.click(
      await screen.findByRole("button", { name: "Delete Finance gets payroll" }),
    );
    expect(confirm).toHaveBeenCalledWith(expect.stringContaining("3 people"));
    await waitFor(() => expect("DELETE /api/v1/access-rules/r1" in sent).toBe(true));
    expect(await screen.findByText(/0 people got access and 3 lost it/)).toBeInTheDocument();
    confirm.mockRestore();
  });
});
