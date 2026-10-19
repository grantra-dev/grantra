import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";
import type { Me, ReviewItem } from "@/lib/api";
import type { AttackDetail, AttackTarget, RoleSuggestion, UnusedAccess } from "@/lib/insights";
import { ok, renderAt, serve } from "@/test-utils";

const ada: Me = {
  identity_id: "ada",
  display_name: "Ada Admin",
  platform_roles: ["grantra-admin"],
  manages_people: true,
  owns_applications: false,
};
const audrey: Me = { ...ada, identity_id: "audrey", platform_roles: ["grantra-auditor"] };

const domainAdmins: AttackTarget = {
  id: "da",
  name: "Domain Admins",
  application: "Active Directory",
  risk: "critical",
  jit_only: false,
  people: 2,
  standing: 2,
  credential: 1,
  outside: 0,
  orphans: 1,
};

const detail: AttackDetail = {
  ...domainAdmins,
  truncated: false,
  orphan_accounts: [
    {
      account_id: "a9",
      account: "frank-admin",
      application: "Active Directory",
      owner: "Frank",
      owner_status: "terminated",
    },
  ],
  paths: [
    {
      identity_id: "ana",
      identity_name: "Ana",
      standing: true,
      outside: false,
      credential: false,
      steps: [
        { kind: "person", id: "ana", name: "Ana" },
        { kind: "role", id: "ops", name: "Ops", edge: "assigned" },
        {
          kind: "entitlement",
          id: "da",
          name: "Domain Admins",
          detail: "Active Directory",
          edge: "includes",
        },
      ],
    },
    {
      identity_id: "dan",
      identity_name: "Dan",
      standing: true,
      outside: false,
      credential: true,
      steps: [
        { kind: "person", id: "dan", name: "Dan" },
        { kind: "vault_account", id: "v1", name: "backup", edge: "requested" },
        {
          kind: "account",
          id: "a1",
          name: "svc-backup",
          detail: "Active Directory",
          edge: "logs_in_as",
        },
        {
          kind: "entitlement",
          id: "da",
          name: "Domain Admins",
          detail: "Active Directory",
          edge: "holds",
        },
      ],
    },
  ],
};

describe("Attack paths", () => {
  it("lists what people reach, and shows every way to one", async () => {
    serve({
      "GET /api/v1/me": ok(audrey),
      "GET /api/v1/attack-paths": ok({ targets: [domainAdmins] }),
      "GET /api/v1/attack-paths/da": ok(detail),
    });
    renderAt("/security/attack-paths");
    await userEvent.click(await screen.findByRole("button", { name: "Domain Admins" }));
    const card = await screen.findByRole("generic", { name: "Attack paths" });
    const paths = await within(card).findAllByRole("list", { name: "Path" });
    expect(paths[1]).toHaveTextContent(
      "Dan requested Vault account backup which logs in as Account svc-backup · Active Directory which holds Entitlement Domain Admins · Active Directory",
    );
    expect(within(card).getByText("Through a shared account")).toBeInTheDocument();
    expect(within(card).getByText(/belonged to Frank \(Terminated\)/)).toBeInTheDocument();
    expect(within(card).getByText(/Make it just-in-time only/)).toBeInTheDocument();
    expect(within(paths[0]!).getByRole("link", { name: "Ana" })).toHaveAttribute(
      "href",
      "/people/ana",
    );
  });

  it("tells an admin what a threatened account reaches", async () => {
    serve({
      "GET /api/v1/me": ok(ada),
      "GET /api/v1/me/inbox": ok({
        approvals: [],
        reviews: [],
        break_glass: [],
        threats: [],
        total: 0,
      }),
      "GET /api/v1/threats": ok({
        open: {},
        sources: [],
        grantra_login: true,
        login_event_types: [],
        token_scope: "sign_in_event.send",
      }),
      "GET /api/v1/threats/alerts?status=open&limit=50": ok({ alerts: [] }),
      "GET /api/v1/threats/alerts/t1": ok({
        id: "t1",
        detection: "impossible_travel",
        title: "Impossible travel",
        severity: "high",
        status: "open",
        identity_id: "dan",
        identity_name: "Dan",
        subject: "dan",
        summary: "Madrid, then Sydney",
        evidence: {},
        occurrences: 1,
        first_seen_at: "2026-10-19T08:00:00Z",
        last_seen_at: "2026-10-19T08:00:00Z",
        created_at: "2026-10-19T08:00:00Z",
      }),
      "GET /api/v1/identities/dan/reach": ok({ targets: [{ ...domainAdmins, paths: [] }] }),
    });
    renderAt("/security/threats?alert=t1");
    const link = await screen.findByRole("link", { name: "Domain Admins" });
    expect(link).toHaveAttribute("href", "/security/attack-paths?target=da");
    expect(screen.getByText("What their account reaches")).toBeInTheDocument();
  });
});

const review = (id: string, person: string, suggestion: ReviewItem["suggestion"]): ReviewItem => ({
  id,
  campaign_id: "c1",
  campaign_name: "Q4 review",
  identity_id: person.toLowerCase(),
  identity_name: person,
  type: "entitlement",
  target_id: `e-${id}`,
  target_name: "Reports",
  application: "Salesforce",
  origin: "request",
  granted_at: "2026-01-01T09:00:00Z",
  decision: "pending",
  suggestion,
});

describe("Suggestions in reviews", () => {
  it("says why, and narrows the list to what Grantra would revoke", async () => {
    serve({
      "GET /api/v1/me": ok(ada),
      "GET /api/v1/review-items?scope=mine&decision=pending&limit=200": ok({
        items: [
          review("i1", "Zed", {
            action: "revoke",
            signals: ["unused", "rare"],
            tracked: true,
            last_used_at: "2026-05-02T09:00:00Z",
            peer_group: "department",
            department: "Sales",
            peers: 12,
            peer_holders: 1,
          }),
          review("i2", "Yan", {
            action: "keep",
            signals: ["common"],
            tracked: false,
            peer_group: "team",
            peers: 4,
            peer_holders: 3,
          }),
          review("i3", "Xia", { signals: [], tracked: false, peers: 0, peer_holders: 0 }),
        ],
      }),
    });
    renderAt("/reviews");
    expect(await screen.findByText("Grantra suggests: revoke")).toBeInTheDocument();
    expect(screen.getByText(/Not used since/)).toBeInTheDocument();
    expect(screen.getByText("Only 1 of 12 people in Sales have it")).toBeInTheDocument();
    expect(
      screen.getByText("3 of 4 people on their manager's team have it too"),
    ).toBeInTheDocument();
    await userEvent.selectOptions(screen.getByLabelText("Grantra suggests"), "revoke");
    expect(screen.getByText("Showing 1 of 3 to review")).toBeInTheDocument();
    expect(screen.queryByText("Yan")).not.toBeInTheDocument();
    await userEvent.selectOptions(screen.getByLabelText("Grantra suggests"), "none");
    expect(screen.getByText("Xia")).toBeInTheDocument();
    expect(screen.queryByText("Zed")).not.toBeInTheDocument();
  });
});

const unused: UnusedAccess = {
  id: "x1",
  eligible: false,
  identity_id: "ana",
  identity_name: "Ana",
  type: "entitlement",
  target_id: "admin",
  name: "Org admin",
  application: "Cloud",
  risk: "critical",
  origin: "manual",
  granted_at: "2026-01-01T09:00:00Z",
};

const sales: RoleSuggestion = {
  name: "Sales",
  conditions: { department: ["Sales"] },
  people: 10,
  have_all: 8,
  entitlements: [
    { id: "e1", name: "Reports", application: "Salesforce", risk: "low", holders: 10 },
    { id: "e2", name: "Leads", application: "Salesforce", risk: "medium", holders: 8 },
  ],
};

describe("Insights", () => {
  it("turns unused standing access into eligibility", async () => {
    const fetch = serve({
      "GET /api/v1/me": ok(ada),
      "GET /api/v1/unused-access?limit=50": ok({ access: [unused], days: 90 }),
      "POST /api/v1/assignments/x1/eligibility": ok({ id: "el1" }),
    });
    renderAt("/governance/insights");
    expect(await screen.findByText("Never")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Make it just in time" }));
    await waitFor(() =>
      expect(fetch.mock.calls.some(([path]) => path === "/api/v1/assignments/x1/eligibility")).toBe(
        true,
      ),
    );
  });

  it("creates a suggested role, and the rule that gives it", async () => {
    const fetch = serve({
      "GET /api/v1/me": ok(ada),
      "GET /api/v1/role-suggestions": ok({ suggestions: [sales] }),
      "POST /api/v1/roles": ok({ id: "r1", name: "Sales team" }),
      "POST /api/v1/access-rules": ok({ id: "rule1" }),
    });
    renderAt("/governance/insights?tab=roles");
    const card = await screen.findByRole("region", { name: "Sales" });
    expect(within(card).getByText(/2 would get access they do not have yet/)).toBeInTheDocument();
    await userEvent.click(within(card).getByRole("checkbox", { name: /Leads/ }));
    // Who lacks part of it is only known for the whole suggestion.
    expect(within(card).queryByText(/would get access/)).not.toBeInTheDocument();
    const name = within(card).getByLabelText("Role name");
    await userEvent.clear(name);
    await userEvent.type(name, "Sales team");
    await userEvent.click(within(card).getByRole("button", { name: "Create role" }));
    expect(
      await within(card).findByText(/Created the Sales team role and its birthright rule/),
    ).toBeInTheDocument();
    const body = (path: string) =>
      JSON.parse(fetch.mock.calls.find(([p]) => p === path)![1]!.body as string);
    expect(body("/api/v1/roles")).toMatchObject({ name: "Sales team", entitlement_ids: ["e1"] });
    expect(body("/api/v1/access-rules")).toMatchObject({
      name: "Sales team",
      conditions: { department: ["Sales"] },
      role_ids: ["r1"],
    });
  });

  it("shows an auditor the findings with nothing to change", async () => {
    serve({
      "GET /api/v1/me": ok(audrey),
      "GET /api/v1/unused-access?limit=50": ok({ access: [unused], days: 90 }),
    });
    renderAt("/governance/insights");
    expect(await screen.findByText("Org admin")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Make it just in time" })).not.toBeInTheDocument();
  });
});
