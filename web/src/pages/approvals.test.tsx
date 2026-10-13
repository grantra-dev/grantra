import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";
import type { AccessRequest, Me } from "@/lib/api";
import { ok, renderAt, serve } from "@/test-utils";

const marta: Me = {
  identity_id: "marta",
  display_name: "Marta Ruiz",
  platform_roles: [],
  manages_people: true,
  owns_applications: false,
};

const request: AccessRequest = {
  id: "q1",
  requester_id: "laura",
  requester_name: "Laura Gómez",
  beneficiary_id: "laura",
  beneficiary_name: "Laura Gómez",
  type: "entitlement",
  target_id: "e1",
  target_name: "Approve payments",
  application: "ERP",
  justification: "Covering Pedro's leave",
  duration_days: 30,
  status: "pending",
  created_at: "2026-10-10T09:00:00Z",
  steps: [
    {
      position: 1,
      kind: "manager",
      approver_id: "marta",
      approver_name: "Marta Ruiz",
      status: "pending",
      due_at: "2026-10-17T09:00:00Z",
    },
    {
      position: 2,
      kind: "owner",
      approver_id: "luis",
      approver_name: "Luis Pérez",
      status: "waiting",
    },
  ],
};

const queue = "GET /api/v1/access-requests?scope=approvals&limit=200";

describe("Approvals", () => {
  it("approves with a comment and says what happens next", async () => {
    let sent: unknown;
    let pending = [request];
    serve({
      "GET /api/v1/me": ok(marta),
      [queue]: () => ({ status: 200, body: { requests: pending } }),
      "POST /api/v1/access-requests/q1/decision": (init) => {
        sent = JSON.parse(init!.body as string);
        pending = [];
        const steps = request.steps.map((s) =>
          s.position === 1
            ? { ...s, status: "approved" as const }
            : { ...s, status: "pending" as const },
        );
        return { status: 200, body: { ...request, steps } };
      },
    });
    renderAt("/approvals");
    expect(await screen.findByText("Laura Gómez wants Approve payments")).toBeInTheDocument();
    expect(screen.getByText("You decide as Laura Gómez's manager.")).toBeInTheDocument();
    await userEvent.type(screen.getByLabelText("Comment"), "ok for the leave");
    await userEvent.click(screen.getByRole("button", { name: "Approve" }));
    expect(await screen.findByText(/It now waits for Luis Pérez/)).toBeInTheDocument();
    expect(sent).toEqual({ decision: "approve", comment: "ok for the leave" });
    await waitFor(() => expect(screen.getByText(/Nothing is waiting for you/)).toBeInTheDocument());
  });

  it("needs a comment to reject", async () => {
    serve({ "GET /api/v1/me": ok(marta), [queue]: ok({ requests: [request] }) });
    renderAt("/approvals");
    const reject = await screen.findByRole("button", { name: "Reject" });
    expect(reject).toBeDisabled();
    await userEvent.type(screen.getByLabelText("Comment"), "use the shared mailbox");
    expect(reject).toBeEnabled();
  });

  it("explains a separation of duties refusal instead of failing silently", async () => {
    serve({
      "GET /api/v1/me": ok(marta),
      [queue]: ok({ requests: [request] }),
      "POST /api/v1/access-requests/q1/decision": () => ({
        status: 409,
        body: { error: "this access would break separation of duties: Payments" },
      }),
    });
    renderAt("/approvals");
    await userEvent.click(await screen.findByRole("button", { name: "Approve" }));
    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent("separation of duties policy in prevent mode (Payments)");
    expect(alert).toHaveTextContent("still pending");
    expect(screen.getByRole("button", { name: "Approve" })).toBeDisabled();
  });
});
