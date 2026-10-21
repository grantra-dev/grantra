import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";
import type { AccessRequest, Me, Requestable } from "@/lib/api";
import type { Plan } from "@/lib/approvals";
import { ok, renderAt, serve, step, type Handler } from "@/test-utils";

const me: Me = {
  identity_id: "me",
  display_name: "Laura Gómez",
  platform_roles: [],
  manages_people: false,
  owns_applications: false,
};

const catalog: Requestable[] = [
  {
    type: "role",
    id: "r1",
    name: "Sales team",
    description: "CRM and pipeline tools",
    jit_only: false,
  },
  {
    type: "entitlement",
    id: "e1",
    name: "Approve payments",
    application: "ERP",
    risk: "high",
    jit_only: false,
  },
  {
    type: "entitlement",
    id: "e2",
    name: "Billing read-only",
    application: "Stripe",
    risk: "low",
    jit_only: false,
  },
];

const pendingRequest: AccessRequest = {
  id: "q1",
  requester_id: "me",
  requester_name: "Laura Gómez",
  beneficiary_id: "me",
  beneficiary_name: "Laura Gómez",
  type: "entitlement",
  target_id: "e2",
  target_name: "Billing read-only",
  application: "Stripe",
  justification: "Monthly close",
  kind: "access",
  status: "pending",
  created_at: "2026-10-10T09:00:00Z",
  steps: [
    step({ position: 1, kind: "manager", status: "pending", due_at: "2026-10-17T09:00:00Z" }, [
      "m",
      "Marta Ruiz",
    ]),
    step({ position: 2, kind: "owner", status: "waiting" }, ["o", "Luis Pérez"]),
  ],
};

// Who approves, as the preview says before sending.
const plan: Plan = {
  risk: "low",
  steps: [
    {
      kind: "manager",
      approvers: [{ id: "m", name: "Marta Ruiz" }],
      required: 1,
      on_timeout: "expire",
    },
    {
      kind: "people",
      label: "Sales leads",
      approvers: [
        { id: "p1", name: "Pia" },
        { id: "p2", name: "Pablo" },
        { id: "p3", name: "Paz" },
      ],
      required: 2,
      timeout_hours: 48,
      on_timeout: "escalate",
    },
  ],
  policy_id: "pol1",
  policy_name: "Sales",
  policy_version: 3,
};

function base(requests: AccessRequest[] = []): Record<string, Handler> {
  return {
    "GET /api/v1/me": ok(me),
    "GET /api/v1/requestable-access": ok({ items: catalog }),
    "GET /api/v1/identities/me/access": ok({ assignments: [] }),
    "GET /api/v1/access-requests?scope=mine&limit=200": ok({ requests }),
  };
}

describe("Request access", () => {
  it("sends a request with a justification and duration, then shows it", async () => {
    let sent: unknown;
    const created = { ...pendingRequest, target_id: "r1", target_name: "Sales team", type: "role" };
    serve({
      ...base(),
      "POST /api/v1/access-requests/preview": ok({ sod_conflicts: [], plan }),
      "POST /api/v1/access-requests": (init) => {
        sent = JSON.parse(init!.body as string);
        return { status: 201, body: created };
      },
    });
    renderAt("/request");
    await userEvent.click(await screen.findByRole("button", { name: "Request Sales team" }));
    const send = screen.getByRole("button", { name: "Send request" });
    expect(send).toBeDisabled();
    await userEvent.type(screen.getByLabelText("Why do you need it?"), "Joining sales");
    await userEvent.selectOptions(screen.getByLabelText("For how long?"), "d:90");
    // Before sending, it says who will approve it.
    const who = await screen.findByRole("region", { name: "Who approves it" });
    expect(within(who).getByText("Marta Ruiz")).toBeInTheDocument();
    expect(within(who).getByText("2 of Pia, Pablo, Paz")).toBeInTheDocument();
    expect(
      within(who).getByText(/48 hours to decide, then it goes to the approvers' managers/),
    ).toBeInTheDocument();
    expect(within(who).getByText("Approval policy “Sales”, version 3")).toBeInTheDocument();
    await waitFor(() => expect(send).toBeEnabled());
    await userEvent.click(send);
    expect(await screen.findByText(/sent\. It now waits/)).toBeInTheDocument();
    expect(sent).toEqual({
      role_id: "r1",
      kind: "access",
      justification: "Joining sales",
      duration_days: 90,
    });
  });

  it("warns about a separation of duties conflict and says an admin must accept it", async () => {
    serve({
      ...base(),
      "POST /api/v1/access-requests/preview": ok({ sod_conflicts: ["Payments"], plan }),
    });
    renderAt("/request");
    expect(await screen.findByText("High risk")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Request Approve payments" }));
    const warning = await screen.findByText(/this access breaks the separation of duties/);
    expect(
      await screen.findByText(
        "Breaks Payments: an admin accepts the risk, with a reason and an end date.",
      ),
    ).toBeInTheDocument();
    expect(warning).toHaveTextContent("Payments");
    expect(warning).toHaveTextContent("a Grantra admin has to accept the risk");
    await userEvent.type(screen.getByLabelText("Why do you need it?"), "Covering a leave");
    expect(screen.getByRole("button", { name: "Send request" })).toBeEnabled();
  });

  it("marks what I already asked for", async () => {
    serve(base([pendingRequest]));
    renderAt("/request");
    expect(await screen.findByText("Requested")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Request Billing read-only" })).toBeNull();
  });
});

describe("My requests", () => {
  it("shows where each request stands and lets me withdraw it", async () => {
    const fetch = serve({
      ...base([pendingRequest]),
      "POST /api/v1/access-requests/q1/cancel": ok({ ...pendingRequest, status: "cancelled" }),
    });
    renderAt("/requests");
    expect(await screen.findByText("Billing read-only")).toBeInTheDocument();
    const flow = screen.getByRole("list", { name: "Approval flow" });
    expect(within(flow).getByText("Manager")).toBeInTheDocument();
    expect(within(flow).getByText("Marta Ruiz")).toBeInTheDocument();
    expect(screen.getByText(/Built-in approval flow/)).toBeInTheDocument();
    expect(screen.getByText(/deciding now/)).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Withdraw" }));
    await waitFor(() =>
      expect(fetch).toHaveBeenCalledWith(
        "/api/v1/access-requests/q1/cancel",
        expect.objectContaining({ method: "POST" }),
      ),
    );
  });
});
