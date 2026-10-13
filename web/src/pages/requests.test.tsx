import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router";
import { describe, expect, it, vi } from "vitest";
import { App } from "@/App";
import type { AccessRequest, Me, Requestable } from "@/lib/api";

const me: Me = {
  identity_id: "me",
  display_name: "Laura Gómez",
  platform_roles: [],
  manages_people: false,
  owns_applications: false,
};

const catalog: Requestable[] = [
  { type: "role", id: "r1", name: "Sales team", description: "CRM and pipeline tools" },
  { type: "entitlement", id: "e1", name: "Approve payments", application: "ERP", risk: "high" },
  { type: "entitlement", id: "e2", name: "Billing read-only", application: "Stripe", risk: "low" },
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
  status: "pending",
  created_at: "2026-10-10T09:00:00Z",
  steps: [
    {
      position: 1,
      kind: "manager",
      approver_id: "m",
      approver_name: "Marta Ruiz",
      status: "pending",
      due_at: "2026-10-17T09:00:00Z",
    },
    {
      position: 2,
      kind: "owner",
      approver_id: "o",
      approver_name: "Luis Pérez",
      status: "waiting",
    },
  ],
};

type Handler = (init?: RequestInit) => { status: number; body: unknown };

function serve(routes: Record<string, Handler>) {
  const fetch = vi.fn(async (path: string, init?: RequestInit) => {
    const key = `${init?.method ?? "GET"} ${path}`;
    const r = routes[key]?.(init) ?? { status: 404, body: { error: `no route ${key}` } };
    return new Response(JSON.stringify(r.body), {
      status: r.status,
      headers: { "Content-Type": "application/json" },
    });
  });
  vi.stubGlobal("fetch", fetch);
  return fetch;
}

const ok = (body: unknown) => () => ({ status: 200, body });

function base(requests: AccessRequest[] = []): Record<string, Handler> {
  return {
    "GET /api/v1/me": ok(me),
    "GET /api/v1/requestable-access": ok({ items: catalog }),
    "GET /api/v1/identities/me/access": ok({ assignments: [] }),
    "GET /api/v1/access-requests?scope=mine&limit=200": ok({ requests }),
  };
}

function renderAt(path: string) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <MemoryRouter initialEntries={[path]}>
        <App />
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

describe("Request access", () => {
  it("sends a request with a justification and duration, then shows it", async () => {
    let sent: unknown;
    const created = { ...pendingRequest, target_id: "r1", target_name: "Sales team", type: "role" };
    serve({
      ...base(),
      "POST /api/v1/access-requests/preview": ok({ sod_conflicts: [] }),
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
    await userEvent.selectOptions(screen.getByLabelText("For how long?"), "90");
    await waitFor(() => expect(send).toBeEnabled());
    await userEvent.click(send);
    expect(await screen.findByText(/sent\. It now waits/)).toBeInTheDocument();
    expect(sent).toEqual({ role_id: "r1", justification: "Joining sales", duration_days: 90 });
  });

  it("explains a separation of duties conflict and blocks the request", async () => {
    serve({
      ...base(),
      "POST /api/v1/access-requests/preview": ok({ sod_conflicts: ["Payments"] }),
    });
    renderAt("/request");
    expect(await screen.findByText("High risk")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Request Approve payments" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("Payments");
    expect(screen.getByRole("button", { name: "Send request" })).toBeDisabled();
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
    expect(screen.getByText("Manager: Marta Ruiz")).toBeInTheDocument();
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
