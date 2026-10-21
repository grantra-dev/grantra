import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";
import type { AccessRequest, Assignment, Identity, Me } from "@/lib/api";
import type { ApprovalPolicy, Plan } from "@/lib/approvals";
import { adminMe as ada, ok, renderAt, serve, step, type Handler } from "@/test-utils";

const audrey: Me = { ...ada, identity_id: "audrey", platform_roles: ["grantra-auditor"] };

const payroll: ApprovalPolicy = {
  id: "p1",
  name: "Payroll",
  position: 1,
  enabled: true,
  conditions: { application_ids: ["app1"], risks: ["high", "critical"] },
  steps: [
    { approver: "manager", level: 2 },
    {
      approver: "people",
      people_ids: ["pia", "pablo"],
      all: true,
      people: [
        { id: "pia", name: "Pia" },
        { id: "pablo", name: "Pablo" },
      ],
    },
  ],
  version: 3,
  updated_by: "identity:ada",
  updated_at: "2026-10-19T08:00:00Z",
};
const lowRisk: ApprovalPolicy = {
  ...payroll,
  id: "p2",
  name: "Low risk",
  position: 2,
  enabled: false,
  conditions: { risks: ["low"] },
  steps: [],
  version: 1,
};
const builtin = {
  ...payroll,
  id: "",
  name: "Built-in flow",
  steps: [{ approver: "manager" }, { approver: "owner" }],
};
const pia: Identity = {
  id: "pia",
  display_name: "Pia",
  kind: "human",
  status: "active",
  attributes: {},
  created_at: "2026-01-01T00:00:00Z",
};

function routes(me: Me, extra: Record<string, Handler> = {}): Record<string, Handler> {
  return {
    "GET /api/v1/me": ok(me),
    "GET /api/v1/me/inbox": ok({
      approvals: [],
      reviews: [],
      break_glass: [],
      threats: [],
      total: 0,
    }),
    "GET /api/v1/approval-policies": ok({ policies: [payroll, lowRisk], builtin }),
    "GET /api/v1/applications": ok({
      applications: [{ id: "app1", name: "Payroll", connector: "csv", entitlement_count: 1 }],
    }),
    "GET /api/v1/roles": ok({ roles: [] }),
    "GET /api/v1/applications/app1/entitlements": ok({
      entitlements: [
        {
          id: "e1",
          application_id: "app1",
          application_name: "Payroll",
          name: "Payroll admin",
          risk: "high",
        },
      ],
    }),
    "GET /api/v1/identities?limit=50&q=pi&status=active&kind=human": ok({ identities: [pia] }),
    ...extra,
  };
}

describe("Governance → Approval flows", () => {
  it("lists the policies in order, then the built-in flow", async () => {
    let order: unknown;
    serve(
      routes(ada, {
        "PUT /api/v1/approval-policies/order": (init) => {
          order = JSON.parse(init!.body as string);
          return { status: 200, body: { policies: [lowRisk, payroll] } };
        },
      }),
    );
    renderAt("/governance/approval-flows");
    const list = await screen.findByRole("region", { name: "Approval policies" });
    expect(await within(list).findByText(/Payroll · high or critical risk/)).toBeInTheDocument();
    expect(
      within(list).getByText(/Manager's manager → Named people: all of Pia, Pablo/),
    ).toBeInTheDocument();
    expect(within(list).getByText("nobody, granted at once")).toBeInTheDocument();
    expect(within(list).getByText("Off")).toBeInTheDocument();
    expect(within(list).getByText("Manager → Owner")).toBeInTheDocument();
    await userEvent.click(within(list).getByRole("button", { name: "Move Payroll down" }));
    await waitFor(() => expect(order).toEqual({ ids: ["p2", "p1"] }));
  });

  it("is read-only for auditors", async () => {
    serve(routes(audrey));
    renderAt("/governance/approval-flows");
    expect(await screen.findByText(/Payroll · high or critical risk/)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Edit" })).toBeNull();
    expect(screen.queryByRole("button", { name: /New policy/ })).toBeNull();
  });

  it("creates a policy with named people", async () => {
    let saved: unknown;
    serve(
      routes(ada, {
        "POST /api/v1/approval-policies": (init) => {
          saved = JSON.parse(init!.body as string);
          return { status: 201, body: payroll };
        },
      }),
    );
    renderAt("/governance/approval-flows");
    await userEvent.click(await screen.findByRole("button", { name: /New policy/ }));
    await userEvent.type(screen.getByLabelText("Name"), "Payroll");
    await userEvent.click(screen.getByLabelText("high"));
    await userEvent.type(
      screen.getByLabelText("Departments of the person it is for"),
      "Finance, Payroll ",
    );
    const second = screen.getByRole("group", { name: "Step 2" });
    await userEvent.selectOptions(within(second).getByLabelText("Who"), "people");
    await userEvent.type(within(second).getByLabelText("People"), "pi");
    await userEvent.click(await within(second).findByRole("button", { name: "Add Pia" }));
    await userEvent.selectOptions(
      within(second).getByLabelText("If nobody decides in time"),
      "escalate",
    );
    await userEvent.click(screen.getByRole("button", { name: "Save policy" }));
    await waitFor(() =>
      expect(saved).toEqual({
        name: "Payroll",
        enabled: true,
        conditions: { risks: ["high"], departments: ["Finance", "Payroll"] },
        steps: [
          { approver: "manager" },
          {
            approver: "people",
            people: [{ id: "pia", name: "Pia" }],
            people_ids: ["pia"],
            on_timeout: "escalate",
          },
        ],
      }),
    );
  });

  it("tries who would approve a request", async () => {
    let asked: unknown;
    const plan: Plan = {
      policy_id: "p1",
      policy_name: "Payroll",
      policy_version: 3,
      risk: "high",
      steps: [
        {
          kind: "admin",
          approvers: [],
          required: 1,
          on_timeout: "expire",
          note: "nobody to ask, so an admin decides",
        },
      ],
    };
    serve(
      routes(ada, {
        "POST /api/v1/approval-policies/simulate": (init) => {
          asked = JSON.parse(init!.body as string);
          return { status: 200, body: { plan, sod_conflicts: ["Payments"] } };
        },
      }),
    );
    renderAt("/governance/approval-flows");
    await userEvent.type(await screen.findByLabelText("If"), "pi");
    await userEvent.click(await screen.findByRole("button", { name: "Pick Pia" }));
    await userEvent.selectOptions(await screen.findByLabelText("asks for"), "entitlement:e1");
    await userEvent.selectOptions(screen.getByLabelText("for"), "30");
    await userEvent.click(screen.getByRole("button", { name: "Who approves it?" }));
    const who = await screen.findByRole("list", { name: "Who approves" });
    expect(within(who).getByText("nobody to ask, so an admin decides")).toBeInTheDocument();
    expect(within(who).getByText(/Breaks Payments/)).toBeInTheDocument();
    expect(screen.getByText("Approval policy “Payroll”, version 3")).toBeInTheDocument();
    expect(asked).toEqual({
      beneficiary_id: "pia",
      entitlement_id: "e1",
      kind: "access",
      duration_days: 30,
    });
  });
});

describe("How access was approved", () => {
  it("opens the approval flow of the request that granted it", async () => {
    const granted: Assignment = {
      id: "a1",
      type: "entitlement",
      target_id: "e1",
      name: "Payroll admin",
      application: "Payroll",
      origin: "request",
      granted_by: "request:q1",
      granted_at: "2026-10-12T09:00:00Z",
      request_id: "q1",
    };
    const request: AccessRequest = {
      id: "q1",
      requester_id: "ada",
      requester_name: "Ada Admin",
      beneficiary_id: "ada",
      beneficiary_name: "Ada Admin",
      type: "entitlement",
      target_id: "e1",
      target_name: "Payroll admin",
      application: "Payroll",
      justification: "Month end",
      kind: "access",
      status: "approved",
      created_at: "2026-10-10T09:00:00Z",
      closed_at: "2026-10-12T09:00:00Z",
      policy_id: "p1",
      policy_name: "Payroll",
      policy_version: 3,
      steps: [
        {
          ...step({
            position: 1,
            kind: "people",
            status: "approved",
            decided_at: "2026-10-12T09:00:00Z",
          }),
          label: "Payroll leads",
          required: 2,
          approvers: [
            { id: "pia", name: "Pia", active: true },
            { id: "pablo", name: "Pablo", active: true },
          ],
          approvals: [
            { identity_id: "pia", name: "Pia", comment: "ok", at: "2026-10-11T09:00:00Z" },
            {
              identity_id: "pablo",
              name: "Pablo",
              comment: "fine for month end",
              at: "2026-10-12T09:00:00Z",
            },
          ],
          escalated_at: "2026-10-11T08:00:00Z",
          escalated_from: ["Marta"],
        },
      ],
    };
    serve({
      "GET /api/v1/me": ok(ada),
      "GET /api/v1/me/inbox": ok({
        approvals: [],
        reviews: [],
        break_glass: [],
        threats: [],
        total: 0,
      }),
      "GET /api/v1/identities/ada/access": ok({ assignments: [granted] }),
      "GET /api/v1/identities/ada/eligibilities": ok({ eligibilities: [] }),
      "GET /api/v1/access-requests/q1": ok(request),
    });
    renderAt("/my-access");
    const open = await screen.findByRole("button", { name: "How it was approved" });
    await userEvent.click(open);
    expect(open).toHaveAttribute("aria-expanded", "true");
    const flow = await screen.findByRole("list", { name: "Approval flow" });
    expect(within(flow).getByText("Asked by Ada Admin")).toBeInTheDocument();
    expect(within(flow).getByText("Payroll leads")).toBeInTheDocument();
    expect(within(flow).getByText("all of Pia ✓, Pablo ✓")).toBeInTheDocument();
    expect(
      within(flow).getByText(/Escalated on .*: Marta did not decide in time/),
    ).toBeInTheDocument();
    expect(within(flow).getByText("Pablo: “fine for month end”")).toBeInTheDocument();
    expect(within(flow).getByText("Granted")).toBeInTheDocument();
    expect(screen.getByText("Approval policy “Payroll”, version 3")).toBeInTheDocument();
  });
});
