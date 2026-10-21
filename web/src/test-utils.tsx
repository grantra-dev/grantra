import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { vi } from "vitest";
import { App } from "@/App";
import type { ApprovalStep } from "@/lib/api";

export type Handler = (init?: RequestInit) => { status: number; body: unknown };

/** Stubs fetch with routes keyed "METHOD path". */
export function serve(routes: Record<string, Handler>) {
  const fetch = vi.fn(async (path: string, init?: RequestInit) => {
    const key = `${init?.method ?? "GET"} ${path}`;
    const r = routes[key]?.(init) ?? { status: 404, body: { error: `no route ${key}` } };
    return new Response(r.status === 204 ? null : JSON.stringify(r.body), {
      status: r.status,
      headers: { "Content-Type": "application/json" },
    });
  });
  vi.stubGlobal("fetch", fetch);
  return fetch;
}

export const ok = (body: unknown) => () => ({ status: 200, body });

export function renderAt(path: string) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <MemoryRouter initialEntries={[path]}>
        <App />
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

/**
 * An approval step as the API sends it, asking one person (approver: their
 * id and name) or, without one, the admins.
 */
export function step(
  s: Pick<ApprovalStep, "position" | "kind" | "status"> & Partial<ApprovalStep>,
  approver?: [id: string, name: string],
): ApprovalStep {
  return {
    approvers: approver ? [{ id: approver[0], name: approver[1], active: true }] : [],
    required: 1,
    approvals: [],
    on_timeout: "expire",
    ...s,
  };
}
