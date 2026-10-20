import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";
import type { Me } from "@/lib/api";
import type { Inbox } from "@/lib/console";
import type { Setting, SettingChange, SettingsPage } from "@/lib/settings";
import { exceptionChoices } from "@/lib/settings";
import { ok, renderAt, serve } from "@/test-utils";

const ada: Me = {
  identity_id: "ada",
  display_name: "Ada Admin",
  platform_roles: ["grantra-admin"],
  manages_people: false,
  owns_applications: false,
};
const audrey: Me = { ...ada, identity_id: "audrey", platform_roles: ["grantra-auditor"] };

const breakGlass: Setting = {
  key: "vault.break_glass_minutes",
  group: "pam",
  module: "pam",
  title: "Longest break-glass check-out",
  help: "Never longer than this.",
  kind: "number",
  unit: "minutes",
  default: 60,
  min: 15,
  max: 480,
  looser: "higher",
  value: 60,
};
const unused: Setting = {
  key: "insights.unused_days",
  group: "insights",
  title: "Unused after",
  help: "Access nobody used for this long.",
  kind: "number",
  unit: "days",
  default: 90,
  min: 30,
  max: 730,
  value: 60,
  updated_by: "identity:ada",
  updated_at: "2026-10-19T08:00:00Z",
};
const change: SettingChange = {
  id: "c1",
  key: breakGlass.key,
  title: breakGlass.title,
  unit: "minutes",
  kind: "number",
  old_value: 60,
  value: 120,
  reason: "night shifts",
  proposed_by: "identity:luis",
  proposed_by_name: "Luis",
  proposed_at: "2026-10-20T08:00:00Z",
  status: "pending",
};
const emptyInbox: Inbox = { approvals: [], reviews: [], break_glass: [], threats: [], total: 0 };

function routes(me: Me, page: SettingsPage, extra = {}) {
  return {
    "GET /api/v1/me": ok(me),
    "GET /api/v1/me/inbox": ok(emptyInbox),
    "GET /api/v1/settings": ok(page),
    ...extra,
  };
}

describe("Settings → General", () => {
  it("groups settings and says which are changed", async () => {
    serve(routes(ada, { settings: [breakGlass, unused], second_admin: false }));
    renderAt("/settings/general");
    const pam = await screen.findByRole("region", { name: "Privileged access" });
    expect(within(pam).getByText("Default")).toBeInTheDocument();
    const insights = screen.getByRole("region", { name: "Insights" });
    expect(within(insights).getByText(/Changed .* · default 90 days/)).toBeInTheDocument();
    expect(
      screen.getByText(/as the only admin, your changes take effect at once/),
    ).toBeInTheDocument();
  });

  it("hides the settings of a module that is off", async () => {
    serve(
      routes(
        { ...ada, disabled_modules: ["pam"] },
        { settings: [breakGlass, unused], second_admin: false },
      ),
    );
    renderAt("/settings/general");
    expect(await screen.findByRole("region", { name: "Insights" })).toBeInTheDocument();
    expect(screen.queryByRole("region", { name: "Privileged access" })).toBeNull();
  });

  it("asks another admin for a less strict value, with a reason", async () => {
    let saved: unknown;
    serve(
      routes(
        ada,
        { settings: [breakGlass], second_admin: true },
        {
          "PUT /api/v1/settings/vault.break_glass_minutes": (init?: RequestInit) => {
            saved = JSON.parse(init!.body as string);
            return { status: 202, body: { ...breakGlass, pending: change } };
          },
        },
      ),
    );
    renderAt("/settings/general");
    const field = await screen.findByRole("spinbutton", { name: "Longest break-glass check-out" });
    await userEvent.clear(field);
    await userEvent.type(field, "120");
    await userEvent.type(
      screen.getByLabelText("Why (for the admin who approves it)"),
      "night shifts",
    );
    await userEvent.click(screen.getByRole("button", { name: "Ask another admin" }));
    await waitFor(() => expect(saved).toEqual({ value: 120, reason: "night shifts" }));
  });

  it("saves a stricter value at once, and refuses one out of range", async () => {
    let saved: unknown;
    serve(
      routes(
        ada,
        { settings: [breakGlass], second_admin: true },
        {
          "PUT /api/v1/settings/vault.break_glass_minutes": (init?: RequestInit) => {
            saved = JSON.parse(init!.body as string);
            return { status: 200, body: { ...breakGlass, value: 30 } };
          },
        },
      ),
    );
    renderAt("/settings/general");
    const field = await screen.findByRole("spinbutton", { name: "Longest break-glass check-out" });
    await userEvent.clear(field);
    await userEvent.type(field, "5");
    expect(screen.getByRole("button", { name: "Save" })).toBeDisabled();
    expect(screen.getByText("Between 15 and 480 minutes.")).toBeInTheDocument();
    await userEvent.clear(field);
    await userEvent.type(field, "30");
    expect(screen.queryByLabelText("Why (for the admin who approves it)")).toBeNull();
    await userEvent.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(saved).toEqual({ value: 30, reason: "" }));
  });

  it("lets a second admin approve, and the proposer withdraw", async () => {
    let decided: unknown;
    const decision = (init?: RequestInit) => {
      decided = JSON.parse(init!.body as string);
      return { status: 200, body: { ...change, status: "approved" } };
    };
    serve(
      routes(
        ada,
        { settings: [{ ...breakGlass, pending: change }], second_admin: true },
        {
          "POST /api/v1/setting-changes/c1/decision": decision,
        },
      ),
    );
    const { unmount } = renderAt("/settings/general");
    expect(await screen.findByText(/asked to change it from 60 minutes to/)).toHaveTextContent(
      "Luis asked to change it from 60 minutes to 120 minutes",
    );
    expect(screen.getByText("Why: night shifts")).toBeInTheDocument();
    expect(
      screen.getByRole("spinbutton", { name: "Longest break-glass check-out" }),
    ).toBeDisabled();
    await userEvent.click(screen.getByRole("button", { name: "Approve" }));
    await waitFor(() => expect(decided).toEqual({ approve: true, comment: "" }));
    unmount();

    serve(
      routes(
        { ...ada, identity_id: "luis" },
        { settings: [{ ...breakGlass, pending: change }], second_admin: true },
        {
          "POST /api/v1/setting-changes/c1/decision": decision,
        },
      ),
    );
    renderAt("/settings/general");
    await userEvent.click(await screen.findByRole("button", { name: "Withdraw" }));
    expect(screen.queryByRole("button", { name: "Approve" })).toBeNull();
    await waitFor(() => expect(decided).toEqual({ approve: false, comment: "" }));
  });

  it("is read-only for auditors", async () => {
    serve(
      routes(audrey, {
        settings: [{ ...breakGlass, pending: change }, unused],
        second_admin: true,
      }),
    );
    renderAt("/settings/general");
    expect(await screen.findByText(/only admins change it/)).toBeInTheDocument();
    expect(await screen.findByRole("spinbutton", { name: "Unused after" })).toBeDisabled();
    expect(screen.queryByRole("button", { name: "Approve" })).toBeNull();
  });

  it("shows changes waiting for you in the inbox", async () => {
    serve({
      "GET /api/v1/me": ok(ada),
      "GET /api/v1/me/inbox": ok({ ...emptyInbox, setting_changes: [change], total: 1 }),
    });
    renderAt("/inbox");
    const section = await screen.findByRole("region", { name: "Setting changes" });
    expect(
      within(section).getByText("Luis wants longest break-glass check-out at 120 minutes"),
    ).toBeInTheDocument();
    expect(within(section).getByRole("link")).toHaveAttribute("href", "/settings/general");
  });
});

describe("exceptionChoices", () => {
  it("never offers more than the tenant allows", () => {
    expect(exceptionChoices()).toEqual([7, 30, 90, 180, 365]);
    expect(exceptionChoices(45)).toEqual([7, 30, 45]);
    expect(exceptionChoices(30, 14)).toEqual([7, 14, 30]);
    expect(exceptionChoices(30, 60)).toEqual([7, 30]);
  });
});
