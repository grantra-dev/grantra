import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";
import type { Me, NotificationSettings } from "@/lib/api";
import { ok, renderAt, serve } from "@/test-utils";

const ana: Me = {
  identity_id: "ana",
  display_name: "Ana Núñez",
  email: "ana@example.com",
  platform_roles: [],
  manages_people: false,
  owns_applications: false,
};

const settings = (enabled: boolean, email_enabled = true): NotificationSettings => ({
  email_enabled,
  has_email: true,
  kinds: [
    {
      kind: "approval_needed",
      title: "Approvals waiting for you",
      description: "x",
      enabled: true,
    },
    { kind: "request_decided", title: "Decisions on requests", description: "y", enabled },
  ],
});

describe("Email notifications", () => {
  it("turns a kind of email off", async () => {
    const fetch = serve({
      "GET /api/v1/me": ok(ana),
      "GET /api/v1/me/notifications": ok(settings(true)),
      "PUT /api/v1/me/notifications/request_decided": ok(settings(false)),
    });
    renderAt("/");
    await userEvent.click(await screen.findByRole("button", { name: /Account of/ }));
    await userEvent.click(await screen.findByRole("menuitem", { name: "Email notifications" }));
    expect(await screen.findByText("ana@example.com", { selector: "strong" })).toBeInTheDocument();
    const decided = await screen.findByRole("checkbox", { name: /Decisions on requests/ });
    expect(decided).toBeChecked();
    await userEvent.click(decided);
    await waitFor(() => expect(decided).not.toBeChecked());
    const put = fetch.mock.calls.find(([, init]) => init?.method === "PUT");
    expect(put?.[1]?.body).toBe(JSON.stringify({ enabled: false }));
    expect(screen.getByRole("checkbox", { name: /Approvals waiting/ })).toBeChecked();
  });

  it("says when this Grantra sends no email", async () => {
    serve({
      "GET /api/v1/me": ok(ana),
      "GET /api/v1/me/notifications": ok(settings(true, false)),
    });
    renderAt("/settings/notifications");
    expect(await screen.findByText(/does not send email yet/)).toBeInTheDocument();
  });
});
