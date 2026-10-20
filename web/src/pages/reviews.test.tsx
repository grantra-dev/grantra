import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";
import type { Campaign, Me, ReviewItem } from "@/lib/api";
import { ok, renderAt, serve, type Handler } from "@/test-utils";

const manager: Me = {
  identity_id: "marta",
  display_name: "Marta Ruiz",
  platform_roles: [],
  manages_people: true,
  owns_applications: false,
};
const admin: Me = { ...manager, identity_id: "ada", platform_roles: ["grantra-admin"] };

const item = (id: string, person: string, access: string): ReviewItem => ({
  id,
  campaign_id: "c1",
  campaign_name: "Q4 review",
  identity_id: person.toLowerCase(),
  identity_name: person,
  type: "entitlement",
  target_id: `e-${id}`,
  target_name: access,
  application: "ERP",
  risk: "high",
  origin: "request",
  granted_at: "2026-06-01T09:00:00Z",
  reviewer_id: "marta",
  reviewer_name: "Marta Ruiz",
  decision: "pending",
});

const campaign: Campaign = {
  id: "c1",
  name: "Q4 review",
  reviewer: "manager",
  application_ids: [],
  on_missing: "keep",
  due_at: "2026-10-27T09:00:00Z",
  status: "active",
  created_by: "identity:ada",
  created_at: "2026-10-13T09:00:00Z",
  progress: { keep: 3, revoke: 1, pending: 4 },
};

const itemsRoute = "GET /api/v1/review-items?scope=mine&decision=pending&limit=200";

function decisions(sent: { id: string; body: unknown }[]): Record<string, Handler> {
  return Object.fromEntries(
    ["i1", "i2"].map((id) => [
      `POST /api/v1/review-items/${id}/decision`,
      (init?: RequestInit) => {
        sent.push({ id, body: JSON.parse(init!.body as string) });
        return { status: 200, body: {} };
      },
    ]),
  );
}

describe("Access reviews", () => {
  it("tells eligibilities apart from access held now", async () => {
    serve({
      "GET /api/v1/me": ok(manager),
      [itemsRoute]: ok({
        items: [
          item("i1", "Laura", "Approve payments"),
          { ...item("i2", "Pedro", "Production admins"), origin: "eligibility" },
        ],
      }),
    });
    renderAt("/reviews");
    expect(await screen.findByRole("cell", { name: /Eligible to activate/ })).toBeInTheDocument();
    expect(screen.getByText(/may turn it on for a few hours/)).toBeInTheDocument();
    expect(screen.getByRole("cell", { name: /^Requested/ })).toBeInTheDocument();
  });

  it("keeps the selected items in bulk with one comment", async () => {
    const sent: { id: string; body: unknown }[] = [];
    serve({
      "GET /api/v1/me": ok(manager),
      [itemsRoute]: ok({
        items: [item("i1", "Laura", "Approve payments"), item("i2", "Pedro", "Create payments")],
      }),
      ...decisions(sent),
    });
    renderAt("/reviews");
    await userEvent.click(await screen.findByLabelText("Select all in Q4 review"));
    await userEvent.type(screen.getByLabelText("Comment for your decisions"), "still in finance");
    await userEvent.click(screen.getByRole("button", { name: "Keep selected (2)" }));
    expect(await screen.findByText("Kept 2 accesses.")).toBeInTheDocument();
    expect(sent).toEqual([
      { id: "i1", body: { decision: "keep", comment: "still in finance" } },
      { id: "i2", body: { decision: "keep", comment: "still in finance" } },
    ]);
    expect(screen.queryByRole("tab", { name: "Campaigns" })).toBeNull();
  });

  it("follows an email to its campaign, and narrows a long list", async () => {
    const sent: { id: string; body: unknown }[] = [];
    serve({
      "GET /api/v1/me": ok(manager),
      [itemsRoute]: ok({
        items: [
          item("i1", "Laura", "Approve payments"),
          { ...item("i2", "Pedro", "Create payments"), risk: "low" },
          { ...item("i3", "Ana", "Reports"), campaign_id: "c2", campaign_name: "Leavers" },
        ],
      }),
      ...decisions(sent),
    });
    renderAt("/reviews?campaign=c1");
    expect(await screen.findByText(/Showing Q4 review only/)).toBeInTheDocument();
    expect(screen.queryByText("Ana")).toBeNull();
    expect(screen.getByText("Showing 2 of 3 to review")).toBeInTheDocument();

    await userEvent.selectOptions(screen.getByLabelText("Risk"), "high");
    expect(screen.queryByText("Pedro")).toBeNull();
    // Bulk actions only reach what is shown.
    await userEvent.click(screen.getByLabelText("Select all in Q4 review"));
    await userEvent.click(screen.getByRole("button", { name: "Keep selected (1)" }));
    await waitFor(() => expect(sent.map((d) => d.id)).toEqual(["i1"]));

    await userEvent.click(screen.getByRole("button", { name: "Show every campaign" }));
    await userEvent.selectOptions(screen.getByLabelText("Risk"), "");
    await userEvent.type(screen.getByLabelText("Find"), "reports");
    expect(await screen.findByText("Ana")).toBeInTheDocument();
    expect(screen.queryByText("Laura")).toBeNull();
  });

  it("asks for confirmation before revoking in bulk", async () => {
    const sent: { id: string; body: unknown }[] = [];
    serve({
      "GET /api/v1/me": ok(manager),
      [itemsRoute]: ok({ items: [item("i1", "Laura", "Approve payments")] }),
      ...decisions(sent),
    });
    renderAt("/reviews");
    await userEvent.click(await screen.findByLabelText("Select Laura: Approve payments"));
    await userEvent.click(screen.getByRole("button", { name: "Revoke selected (1)" }));
    expect(sent).toEqual([]);
    await userEvent.click(screen.getByRole("button", { name: "Confirm: revoke 1" }));
    await waitFor(() =>
      expect(sent).toEqual([{ id: "i1", body: { decision: "revoke", comment: "" } }]),
    );
  });

  it("shows admins campaign progress and launches a campaign", async () => {
    let launched: unknown;
    serve({
      "GET /api/v1/me": ok(admin),
      "GET /api/v1/review-campaigns": ok({ campaigns: [campaign] }),
      "GET /api/v1/applications": ok({
        applications: [
          { id: "g", name: "Grantra", connector: "grantra" },
          { id: "erp", name: "ERP", connector: "scim" },
        ],
      }),
      "POST /api/v1/review-campaigns": (init) => {
        launched = JSON.parse(init!.body as string);
        return { status: 201, body: campaign };
      },
      "GET /api/v1/settings": ok({
        settings: [{ key: "reviews.default_due_days", value: 21 }],
        second_admin: false,
      }),
    });
    renderAt("/reviews?tab=campaigns");
    expect(await screen.findByText("4 of 8 reviewed · 3 kept · 1 revoked")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "New campaign" }));
    await userEvent.type(screen.getByLabelText("Name"), "ERP owners");
    await userEvent.selectOptions(screen.getByLabelText("Who reviews"), "owner");
    expect(await screen.findByLabelText("ERP")).toBeInTheDocument();
    expect(screen.queryByLabelText("Grantra")).toBeNull();
    await userEvent.click(screen.getByLabelText("ERP"));
    // The tenant's default length is offered first, and left to the server.
    expect(
      await screen.findByRole("option", { name: "21 days (the default)" }),
    ).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Launch campaign" }));
    await waitFor(() =>
      expect(launched).toEqual({
        name: "ERP owners",
        reviewer: "owner",
        application_ids: ["erp"],
        on_missing: "keep",
        due_in_days: 0,
      }),
    );
  });
});
