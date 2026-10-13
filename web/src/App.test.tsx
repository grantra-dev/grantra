import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router";
import { describe, expect, it, vi } from "vitest";
import { App } from "./App";
import type { Assignment, Me } from "./lib/api";

const employee: Me = {
  identity_id: "11111111-1111-1111-1111-111111111111",
  display_name: "Laura Gómez",
  email: "laura@example.com",
  platform_roles: [],
  manages_people: false,
  owns_applications: false,
};

const assignments: Assignment[] = [
  {
    id: "a1",
    type: "entitlement",
    target_id: "e1",
    name: "Developers",
    application: "GitHub",
    origin: "birthright",
    rule_name: "Engineering joiners",
    granted_by: "system:access-rules",
    granted_at: "2026-09-01T09:00:00Z",
  },
  {
    id: "a2",
    type: "entitlement",
    target_id: "e2",
    name: "Billing read-only",
    application: "Stripe",
    origin: "request",
    granted_by: "identity:22222222-2222-2222-2222-222222222222",
    granted_at: "2026-10-01T09:00:00Z",
    expires_at: "2027-01-01T00:00:00Z",
  },
];

function serve(routes: Record<string, { status: number; body: unknown }>) {
  vi.stubGlobal(
    "fetch",
    vi.fn(async (path: string) => {
      const r = routes[path] ?? { status: 404, body: { error: "not found" } };
      return new Response(JSON.stringify(r.body), {
        status: r.status,
        headers: { "Content-Type": "application/json" },
      });
    }),
  );
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

describe("App", () => {
  it("offers sign-in, coming back to the same page, when there is no session", async () => {
    serve({ "/api/v1/me": { status: 401, body: {} } });
    renderAt("/my-access");
    const link = await screen.findByRole("link", { name: "Sign in" });
    expect(link).toHaveAttribute("href", "/auth/login?return_to=%2Fmy-access");
  });

  it("shows an employee only the sections every employee has", async () => {
    serve({ "/api/v1/me": { status: 200, body: employee } });
    renderAt("/");
    expect(await screen.findByRole("heading", { name: "Hi, Laura" })).toBeInTheDocument();
    const nav = screen.getByRole("navigation", { name: "Main" });
    expect(nav).toHaveTextContent("My access");
    expect(nav).toHaveTextContent("Request access");
    expect(nav).not.toHaveTextContent("Approvals");
    expect(nav).not.toHaveTextContent("Administration");
  });

  it("shows managers approvals and admins administration", async () => {
    serve({
      "/api/v1/me": {
        status: 200,
        body: { ...employee, manages_people: true, platform_roles: ["grantra-admin"] },
      },
    });
    renderAt("/");
    const nav = await screen.findByRole("navigation", { name: "Main" });
    expect(nav).toHaveTextContent("Approvals");
    expect(nav).toHaveTextContent("Administration");
    expect(nav).toHaveTextContent("Audit log");
  });

  it("does not route to sections the person cannot see", async () => {
    serve({ "/api/v1/me": { status: 200, body: employee } });
    renderAt("/admin");
    expect(await screen.findByRole("heading", { name: "Page not found" })).toBeInTheDocument();
  });

  it("lists my access with why I have it", async () => {
    serve({
      "/api/v1/me": { status: 200, body: employee },
      [`/api/v1/identities/${employee.identity_id}/access`]: {
        status: 200,
        body: { assignments },
      },
    });
    renderAt("/my-access");
    expect(await screen.findByText("Developers")).toBeInTheDocument();
    expect(screen.getByText("Rule: Engineering joiners")).toBeInTheDocument();
    expect(screen.getByText("Requested and approved")).toBeInTheDocument();
    expect(screen.getByText("No end date")).toBeInTheDocument();

    await userEvent.type(screen.getByRole("searchbox"), "stripe");
    expect(screen.queryByText("Developers")).not.toBeInTheDocument();
    expect(screen.getByText("Billing read-only")).toBeInTheDocument();
  });
});
