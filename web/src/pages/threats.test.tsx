import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import type { Me } from "@/lib/api";
import type { Inbox } from "@/lib/console";
import type { Detection, ThreatAlert, ThreatsOverview } from "@/lib/threats";
import { ok, renderAt, serve } from "@/test-utils";

const ada: Me = {
  identity_id: "ada",
  display_name: "Ada Admin",
  platform_roles: ["grantra-admin"],
  manages_people: false,
  owns_applications: false,
};
const audrey: Me = { ...ada, identity_id: "audrey", platform_roles: ["grantra-auditor"] };

const travel: ThreatAlert = {
  id: "t1",
  detection: "impossible_travel",
  title: "Impossible travel",
  severity: "high",
  status: "open",
  identity_id: "luis",
  identity_name: "Luis Pérez",
  identity_status: "active",
  subject: "luis",
  summary: "Signed in from Madrid, then from Sydney 2 hours later",
  evidence: { event_id: 7, ip: "198.51.100.4", country: "AU", distance_km: 17000 },
  occurrences: 1,
  first_seen_at: "2026-10-19T08:00:00Z",
  last_seen_at: "2026-10-19T08:00:00Z",
  created_at: "2026-10-19T08:00:00Z",
};

const overview: ThreatsOverview = {
  open: { high: 1 },
  sources: [],
  grantra_login: true,
  login_event_types: ["LOGIN", "LOGIN_ERROR"],
  token_scope: "sign_in_event.send",
};

const inbox: Inbox = {
  approvals: [],
  reviews: [],
  break_glass: [],
  threats: [
    {
      id: "t1",
      title: "Impossible travel",
      summary: travel.summary,
      severity: "high",
      last_seen_at: travel.last_seen_at,
    },
  ],
  total: 1,
};

const threatRoutes = (me: Me, alert = travel) => ({
  "GET /api/v1/me": ok(me),
  "GET /api/v1/me/inbox": ok(inbox),
  "GET /api/v1/threats": ok(overview),
  "GET /api/v1/threats/alerts?status=open": ok({ alerts: [alert] }),
  [`GET /api/v1/threats/alerts/${alert.id}`]: ok(alert),
});

/** The open alert's card, once it has loaded. */
async function loadedAlert() {
  const card = await screen.findByRole("generic", { name: "Alert" });
  await within(card).findByRole("button", { name: "Close" });
  return card;
}

describe("Threats", () => {
  it("takes an admin from the inbox to the alert, and responds", async () => {
    const fetch = serve({
      ...threatRoutes(ada),
      "POST /api/v1/identities/luis/threat-responses": () => ({
        status: 200,
        body: {
          id: "r1",
          identity_id: "luis",
          alert_id: "t1",
          action: "revoke_sessions",
          actor: "identity:ada",
          details: {},
          created_at: "2026-10-19T08:05:00Z",
        },
      }),
    });
    renderAt("/inbox");
    const task = await screen.findByRole("link", { name: /Impossible travel/ });
    expect(task).toHaveAttribute("href", "/security/threats?alert=t1");
    const nav = screen.getByRole("navigation", { name: "Main" });
    expect(within(nav).getByText("Security")).toBeInTheDocument();
    expect(within(nav).getByRole("link", { name: /Threats/ })).toHaveTextContent("1");

    await userEvent.click(task);
    const detail = await loadedAlert();
    expect(within(detail).getByText("198.51.100.4")).toBeInTheDocument();
    expect(within(detail).getByText("Distance km")).toBeInTheDocument();
    // Ids are for machines, not for the page.
    expect(within(detail).queryByText("Event id")).not.toBeInTheDocument();

    await userEvent.type(within(detail).getByLabelText("Reason"), "not him");
    await userEvent.click(within(detail).getByRole("button", { name: "End sessions" }));
    expect(await within(detail).findByText(/Done: End sessions/)).toBeInTheDocument();
    const post = fetch.mock.calls.find(
      ([path]) => path === "/api/v1/identities/luis/threat-responses",
    );
    expect(JSON.parse(post![1]!.body as string)).toEqual({
      action: "revoke_sessions",
      alert_id: "t1",
      reason: "not him",
    });
  });

  it("asks for a note before dismissing, and confirms a suspension", async () => {
    const fetch = serve({
      ...threatRoutes(ada),
      "POST /api/v1/threats/alerts/t1/close": ok({ ...travel, status: "dismissed" }),
      "POST /api/v1/identities/luis/threat-responses": ok({
        id: "r2",
        identity_id: "luis",
        action: "suspend",
        actor: "identity:ada",
        details: {},
        created_at: "2026-10-19T08:05:00Z",
      }),
    });
    const confirm = vi.spyOn(window, "confirm").mockReturnValue(false);
    renderAt("/security/threats?alert=t1");
    const detail = await loadedAlert();
    const dismiss = within(detail).getByRole("button", { name: "Dismiss" });
    expect(dismiss).toBeDisabled();
    await userEvent.type(within(detail).getByLabelText("Note"), "He is in Sydney this week");
    await userEvent.click(dismiss);
    await waitFor(() =>
      expect(fetch.mock.calls.some(([path]) => path === "/api/v1/threats/alerts/t1/close")).toBe(
        true,
      ),
    );
    const close = fetch.mock.calls.find(([path]) => path === "/api/v1/threats/alerts/t1/close");
    expect(JSON.parse(close![1]!.body as string)).toEqual({
      status: "dismissed",
      note: "He is in Sydney this week",
    });

    await userEvent.selectOptions(within(detail).getByLabelText("Action"), "suspend");
    await userEvent.click(within(detail).getByRole("button", { name: "Suspend" }));
    expect(confirm).toHaveBeenCalled();
    expect(fetch.mock.calls.some(([path]) => String(path).includes("threat-responses"))).toBe(
      false,
    );
    confirm.mockRestore();
  });

  it("does not offer an admin to act on an alert about themselves", async () => {
    serve(threatRoutes(ada, { ...travel, identity_id: "ada", identity_name: "Ada Admin" }));
    renderAt("/security/threats?alert=t1");
    const detail = await loadedAlert();
    expect(within(detail).getByText(/another admin looks into it/)).toBeInTheDocument();
    expect(within(detail).queryByRole("button", { name: "Resolve" })).not.toBeInTheDocument();
  });

  it("shows an auditor the alerts with nothing to change", async () => {
    serve(threatRoutes(audrey));
    renderAt("/security/threats?alert=t1");
    const detail = await loadedAlert();
    expect(within(detail).getByText("Luis Pérez")).toBeInTheDocument();
    expect(within(detail).queryByRole("form", { name: "Respond" })).not.toBeInTheDocument();
    expect(within(detail).queryByRole("button", { name: "Resolve" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Look now" })).not.toBeInTheDocument();
  });

  it("tunes a detection, offering only the responses it allows", async () => {
    const spray: Detection = {
      id: "brute_force",
      title: "Password guessing",
      description: "Many wrong passwords for one person.",
      severity: "medium",
      params: [
        { name: "failures", description: "Wrong passwords", default: 10, min: 3, max: 1000 },
        { name: "window_minutes", description: "Within minutes", default: 15, min: 1, max: 1440 },
      ],
      auto_responses: ["require_mfa"],
      enabled: true,
      auto_response: "none",
      values: { failures: 10, window_minutes: 15 },
    };
    const fetch = serve({
      ...threatRoutes(ada),
      "GET /api/v1/threats/detections": ok({ detections: [spray] }),
      "PUT /api/v1/threats/detections/brute_force": ok({ ...spray, auto_response: "require_mfa" }),
    });
    renderAt("/security/threats?tab=detections");
    const form = await screen.findByRole("form", { name: "Password guessing" });
    const auto = within(form).getByLabelText("Respond on its own");
    expect(
      within(auto)
        .getAllByRole("option")
        .map((o) => o.textContent),
    ).toEqual(["No, only alert", "Require a second factor"]);
    const failures = within(form).getByLabelText("Wrong passwords");
    await userEvent.clear(failures);
    await userEvent.type(failures, "20");
    await userEvent.selectOptions(auto, "require_mfa");
    await userEvent.click(within(form).getByRole("button", { name: "Save" }));
    await waitFor(() =>
      expect(fetch.mock.calls.some(([, init]) => init?.method === "PUT")).toBe(true),
    );
    const put = fetch.mock.calls.find(([, init]) => init?.method === "PUT");
    expect(JSON.parse(put![1]!.body as string)).toEqual({
      enabled: true,
      severity: "medium",
      auto_response: "require_mfa",
      values: { failures: 20, window_minutes: 15 },
    });
  });

  it("is gone when the company turns threat detection off", async () => {
    serve({ ...threatRoutes({ ...ada, disabled_modules: ["itdr"] }) });
    renderAt("/inbox");
    const nav = await screen.findByRole("navigation", { name: "Main" });
    expect(within(nav).queryByText("Security")).not.toBeInTheDocument();
  });

  it("loads sign-ins page by page, from after the last one shown", async () => {
    const page = (from: number, n: number) =>
      Array.from({ length: n }, (_, i) => ({
        id: from - i,
        source: "okta",
        occurred_at: "2026-10-19T08:00:00Z",
        username: `user${from - i}`,
        outcome: "failure" as const,
      }));
    const fetch = serve({
      ...threatRoutes(ada),
      "GET /api/v1/sign-in-events?limit=50": ok({ events: page(100, 50) }),
      "GET /api/v1/sign-in-events?before=51&limit=50": ok({ events: page(50, 3) }),
    });
    renderAt("/security/threats?tab=sign-ins");
    expect(await screen.findByText("user51")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Show more" }));
    expect(await screen.findByText("user48")).toBeInTheDocument();
    // A short page is the last one.
    expect(screen.queryByRole("button", { name: "Show more" })).not.toBeInTheDocument();
    expect(
      fetch.mock.calls.filter(([path]) => String(path).startsWith("/api/v1/sign-in-events")),
    ).toHaveLength(2);
  });
});
