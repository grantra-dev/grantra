import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";
import type { Me, Requestable } from "@/lib/api";
import type { Home, Inbox } from "@/lib/console";
import type { VaultAccount } from "@/lib/vault";
import { ok, renderAt, serve } from "@/test-utils";

const marta: Me = {
  identity_id: "marta",
  display_name: "Marta Ruiz",
  platform_roles: [],
  manages_people: true,
  owns_applications: false,
};
const ada: Me = { ...marta, identity_id: "ada", platform_roles: ["grantra-admin"] };
const audrey: Me = { ...marta, identity_id: "audrey", platform_roles: ["grantra-auditor"] };

const inbox: Inbox = {
  approvals: [
    {
      id: "q1",
      beneficiary_name: "Laura Gómez",
      target_name: "Approve payments",
      application: "ERP",
      kind: "access",
      created_at: "2026-10-10T09:00:00Z",
      due_at: "2026-10-17T09:00:00Z",
    },
  ],
  reviews: [
    { campaign_id: "c1", campaign_name: "Q4 review", due_at: "2026-10-27T09:00:00Z", pending: 3 },
  ],
  break_glass: [],
  threats: [],
  total: 4,
};
const empty: Inbox = { approvals: [], reviews: [], break_glass: [], threats: [], total: 0 };
const quiet: Home = { expiring_access: [], pending_requests: 0 };

describe("Inbox", () => {
  it("opens each task exactly and counts them in the menu", async () => {
    serve({ "GET /api/v1/me": ok(marta), "GET /api/v1/me/inbox": ok(inbox) });
    renderAt("/inbox");
    const approval = await screen.findByRole("link", {
      name: /Laura Gómez wants Approve payments/,
    });
    expect(approval).toHaveAttribute("href", "/approvals?request=q1");
    expect(screen.getByRole("link", { name: /Q4 review/ })).toHaveAttribute(
      "href",
      "/reviews?campaign=c1",
    );
    expect(screen.getByText(/Overdue since/)).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Inbox, 4 waiting" })).toBeInTheDocument();
    const nav = screen.getByRole("navigation", { name: "Main" });
    expect(within(nav).getByRole("link", { name: /Approvals/ })).toHaveTextContent("1");
    expect(within(nav).getByRole("link", { name: /Access reviews/ })).toHaveTextContent("3");
  });

  it("says when nothing waits", async () => {
    serve({ "GET /api/v1/me": ok(marta), "GET /api/v1/me/inbox": ok(empty) });
    renderAt("/inbox");
    expect(await screen.findByText(/Nothing is waiting for you/)).toBeInTheDocument();
    expect(screen.queryByRole("link", { name: /Inbox, \d+ waiting/ })).not.toBeInTheDocument();
  });
});

describe("Home", () => {
  it("shows an admin what needs fixing today", async () => {
    serve({
      "GET /api/v1/me": ok(ada),
      "GET /api/v1/me/inbox": ok(empty),
      "GET /api/v1/me/home": ok({
        ...quiet,
        expiring_access: [
          { name: "Reports", application: "BI", expires_at: "2026-10-20T00:00:00Z" },
        ],
        governance: {
          failed_runs: [
            {
              kind: "aggregation",
              id: "crm",
              name: "CRM",
              error: "401",
              at: "2026-10-18T08:00:00Z",
            },
          ],
          orphans: [{ application_id: "crm", application: "CRM", count: 2 }],
          undescribed: 1,
          open_violations: 0,
        },
      } satisfies Home),
    });
    renderAt("/");
    expect(await screen.findByRole("link", { name: "1 connection is failing" })).toHaveAttribute(
      "href",
      "/governance/applications/crm?tab=runs",
    );
    expect(screen.getByRole("link", { name: "2 accounts belong to nobody" })).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "1 access has no description" })).toBeInTheDocument();
    expect(screen.getByRole("link", { name: /ask for more time/i })).toHaveAttribute(
      "href",
      "/request?q=Reports",
    );
    expect(screen.queryByText(/violations? (is|are) open/)).not.toBeInTheDocument();
    expect(screen.queryByText("All clear")).not.toBeInTheDocument();
  });

  it("is all clear when nothing needs anyone", async () => {
    serve({
      "GET /api/v1/me": ok(marta),
      "GET /api/v1/me/inbox": ok(empty),
      "GET /api/v1/me/home": ok(quiet),
    });
    renderAt("/");
    expect(await screen.findByText("All clear")).toBeInTheDocument();
  });
});

const account: VaultAccount = {
  id: "v1",
  name: "prod-db root",
  username: "root",
  address: "prod-db:5432",
  kind: "password",
  entitlement_id: "e1",
  entitlement_name: "Use prod-db root",
  risk: "critical",
  checkout_minutes: 60,
  exclusive: false,
  rotate_after_checkin: true,
  can_rotate: true,
  rotation_failures: 0,
  version: 2,
  secret_at: "2026-10-16T10:00:00Z",
  checkouts: [],
  held: true,
  responder: false,
  break_glass: false,
  created_at: "2026-10-16T10:00:00Z",
};

describe("Search", () => {
  it("opens with ⌘K and takes you to the exact account", async () => {
    serve({
      "GET /api/v1/me": ok(marta),
      "GET /api/v1/me/inbox": ok(empty),
      "GET /api/v1/me/home": ok(quiet),
      "GET /api/v1/search?q=prod": ok({
        results: [{ kind: "vault_account", id: "v1", title: "prod-db root", detail: "root" }],
      }),
      "GET /api/v1/vault/accounts": ok({ enabled: true, accounts: [account] }),
    });
    renderAt("/");
    await screen.findByText("All clear");
    await userEvent.keyboard("{Meta>}k{/Meta}");
    const box = await screen.findByRole("combobox");
    await userEvent.type(box, "prod");
    expect(await screen.findByRole("option", { name: /prod-db root/ })).toBeInTheDocument();
    await userEvent.keyboard("{Enter}");
    await screen.findByRole("heading", { name: "Vault" });
    await waitFor(() =>
      expect(document.querySelector('[data-focus="v1"]')?.className).toMatch(/ring-2/),
    );
  });

  it("finds pages by name, and moves with the arrow keys", async () => {
    serve({
      "GET /api/v1/me": ok(marta),
      "GET /api/v1/me/inbox": ok(empty),
      "GET /api/v1/me/home": ok(quiet),
      "GET /api/v1/search?q=my%20req": ok({ results: [] }),
      "GET /api/v1/access-requests?scope=mine&limit=200": ok({ requests: [] }),
    });
    renderAt("/");
    await screen.findByText("All clear");
    await userEvent.click(screen.getByRole("button", { name: "Search Grantra" }));
    await userEvent.type(screen.getByRole("combobox"), "my req");
    const option = await screen.findByRole("option", { name: /My requests/ });
    expect(option).toHaveAttribute("aria-selected", "true");
    await userEvent.keyboard("{Enter}");
    expect(await screen.findByRole("heading", { name: "My requests" })).toBeInTheDocument();
    expect(screen.queryByRole("combobox")).not.toBeInTheDocument();
  });
});

describe("Modules", () => {
  it("hides the pages of a module the company turned off", async () => {
    serve({
      "GET /api/v1/me": ok({ ...marta, disabled_modules: ["pam"] }),
      "GET /api/v1/me/inbox": ok(empty),
      "GET /api/v1/me/home": ok(quiet),
    });
    renderAt("/");
    const nav = await screen.findByRole("navigation", { name: "Main" });
    expect(within(nav).getByRole("link", { name: /My access/ })).toBeInTheDocument();
    expect(within(nav).queryByRole("link", { name: /Vault/ })).not.toBeInTheDocument();
  });

  it("lets an admin turn one off", async () => {
    let enabled = true;
    const fetch = serve({
      "GET /api/v1/me": ok(ada),
      "GET /api/v1/me/inbox": ok(empty),
      "GET /api/v1/modules": () => ({
        status: 200,
        body: {
          modules: [
            { id: "pam", title: "Privileged access", description: "Vault and gateways", enabled },
          ],
        },
      }),
      "PUT /api/v1/modules/pam": (init) => {
        enabled = (JSON.parse(init!.body as string) as { enabled: boolean }).enabled;
        return { status: 200, body: {} };
      },
    });
    renderAt("/settings/modules");
    const toggle = await screen.findByRole("switch", { name: "Privileged access" });
    expect(toggle).toBeChecked();
    await userEvent.click(toggle);
    await waitFor(() => expect(toggle).not.toBeChecked());
    const put = fetch.mock.calls.find(([, init]) => init?.method === "PUT");
    expect(put?.[1]?.body).toBe(JSON.stringify({ enabled: false }));
  });
});

const crm = {
  id: "crm",
  name: "CRM",
  connector: "manual",
  config: null,
  provisioning: null,
  entitlement_count: 0,
  created_at: "2026-10-01T00:00:00Z",
};

describe("Read-only pages for auditors", () => {
  it("shows an auditor governance with nothing to change", async () => {
    serve({
      "GET /api/v1/me": ok(audrey),
      "GET /api/v1/me/inbox": ok(empty),
      "GET /api/v1/applications": ok({ applications: [crm] }),
      "GET /api/v1/applications/crm/entitlements": ok({ entitlements: [] }),
    });
    renderAt("/governance/applications");
    expect(await screen.findByRole("link", { name: "CRM" })).toBeInTheDocument();
    expect(screen.getByText(/only admins change it/)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Add application" })).not.toBeInTheDocument();

    await userEvent.click(screen.getByRole("link", { name: "CRM" }));
    expect(await screen.findByRole("heading", { name: "CRM" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /Load accounts/ })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Add entitlement" })).not.toBeInTheDocument();
  });

  it("lets an admin change them", async () => {
    serve({
      "GET /api/v1/me": ok(ada),
      "GET /api/v1/me/inbox": ok(empty),
      "GET /api/v1/applications": ok({ applications: [crm] }),
    });
    renderAt("/governance/applications");
    expect(await screen.findByRole("button", { name: "Add application" })).toBeInTheDocument();
    expect(screen.queryByText(/only admins change it/)).not.toBeInTheDocument();
  });
});

describe("Connector gallery", () => {
  it("connects an application in three steps and tests it", async () => {
    const sent: Record<string, unknown> = {};
    serve({
      "GET /api/v1/me": ok(ada),
      "GET /api/v1/me/inbox": ok(empty),
      "GET /api/v1/applications": ok({ applications: [crm] }),
      "PUT /api/v1/applications/crm/connector": (init) => {
        sent.connector = JSON.parse(init!.body as string);
        return { status: 204, body: null };
      },
      "POST /api/v1/applications/crm/aggregations": ok({
        id: "r1",
        application_id: "crm",
        kind: "aggregation",
        actor: "identity:ada",
        status: "succeeded",
        counts: { accounts: 12 },
        started_at: "2026-10-18T09:00:00Z",
      }),
    });
    renderAt("/settings/connectors");
    await userEvent.click(await screen.findByRole("button", { name: "Connect SCIM 2.0" }));
    expect(screen.getByText("Microsoft Entra ID")).toBeInTheDocument();
    await userEvent.selectOptions(screen.getByLabelText("Application"), "crm");
    await userEvent.click(screen.getByRole("button", { name: "Next" }));

    const save = screen.getByRole("button", { name: "Save and continue" });
    expect(save).toBeDisabled();
    await userEvent.type(screen.getByLabelText("SCIM endpoint"), "https://crm.example.com/scim/v2");
    await userEvent.type(screen.getByLabelText("Bearer token"), "env:GRANTRA_SECRET_CRM");
    await userEvent.click(save);
    expect(sent.connector).toEqual({
      kind: "scim",
      config: { base_url: "https://crm.example.com/scim/v2", token: "env:GRANTRA_SECRET_CRM" },
    });

    await userEvent.click(await screen.findByRole("button", { name: "Test: read accounts now" }));
    expect(await screen.findByText(/It works/)).toBeInTheDocument();
    expect(screen.getByText(/12/)).toBeInTheDocument();
  });

  it("changes a connector, keeping the secret saved before", async () => {
    const okta = {
      ...crm,
      connector: "okta",
      config: { org_url: "https://acme.okta.com", token: "secret:s1", apps: false },
    };
    let sent: unknown;
    serve({
      "GET /api/v1/me": ok(ada),
      "GET /api/v1/me/inbox": ok(empty),
      "GET /api/v1/applications": ok({ applications: [okta] }),
      "GET /api/v1/applications/crm/entitlements": ok({ entitlements: [] }),
      "PUT /api/v1/applications/crm/connector": (init) => {
        sent = JSON.parse(init!.body as string);
        return { status: 204, body: null };
      },
    });
    renderAt("/governance/applications/crm");
    expect(await screen.findByText("Kept encrypted by Grantra")).toBeInTheDocument();
    expect(screen.queryByText("secret:s1")).not.toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Change settings" }));
    const address = screen.getByLabelText("Okta address");
    await userEvent.clear(address);
    await userEvent.type(address, "https://acme.oktapreview.com");
    expect(screen.getByLabelText("API token")).toHaveValue("");
    await userEvent.click(screen.getByRole("button", { name: "Save settings" }));
    await waitFor(() =>
      expect(sent).toEqual({
        kind: "okta",
        config: { org_url: "https://acme.oktapreview.com", token: "secret:s1", apps: false },
      }),
    );
  });

  it("shows an auditor the gallery without connecting", async () => {
    serve({
      "GET /api/v1/me": ok(audrey),
      "GET /api/v1/me/inbox": ok(empty),
      "GET /api/v1/applications": ok({ applications: [] }),
    });
    renderAt("/settings/connectors");
    expect(await screen.findByText("LDAP / Active Directory")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /^Connect / })).not.toBeInTheDocument();
  });
});

describe("Request access", () => {
  const item = (over: Partial<Requestable>): Requestable => ({
    type: "entitlement",
    id: "e1",
    name: "Sales reps",
    application: "CRM",
    jit_only: false,
    ...over,
  });

  it("says how many teammates hold each thing, and which have no description", async () => {
    serve({
      "GET /api/v1/me": ok(marta),
      "GET /api/v1/me/inbox": ok(empty),
      "GET /api/v1/requestable-access": ok({
        team_size: 5,
        items: [
          item({ team_holders: 3, description: "Read and edit leads" }),
          item({ id: "e2", name: "grp_fin_x2" }),
        ],
      }),
      "GET /api/v1/identities/marta/access": ok({ assignments: [] }),
      "GET /api/v1/access-requests?scope=mine&limit=200": ok({ requests: [] }),
    });
    renderAt("/request?q=crm");
    expect(await screen.findByText("3 of your 5 teammates have this")).toBeInTheDocument();
    expect(screen.getByText(/No description yet/)).toBeInTheDocument();
    expect(screen.getByRole("searchbox")).toHaveValue("crm");
  });
});
