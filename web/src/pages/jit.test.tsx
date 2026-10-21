import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";
import { PlatformRole, type AccessRequest, type Me } from "@/lib/api";
import type { Eligibility, Standing } from "@/lib/jit";
import type { VaultAccount, VaultCheckout } from "@/lib/vault";
import { ok, renderAt, serve, step } from "@/test-utils";

const ana: Me = {
  identity_id: "ana",
  display_name: "Ana Núñez",
  platform_roles: [],
  manages_people: false,
  owns_applications: false,
};
const admin: Me = { ...ana, identity_id: "adm", platform_roles: [PlatformRole.Admin] };

const eligibility: Eligibility = {
  id: "el1",
  identity_id: "ana",
  type: "entitlement",
  target_id: "e1",
  name: "Org admins",
  application: "GitHub",
  max_activation_hours: 2,
  activation_approval: false,
  granted_by: "identity:adm",
  granted_at: "2026-10-16T09:00:00Z",
};

const body = (init?: RequestInit) => JSON.parse(init!.body as string) as unknown;

describe("Just-in-time access", () => {
  it("activates an eligibility for the hours chosen, with a reason", async () => {
    let sent: unknown;
    let list = [eligibility];
    serve({
      "GET /api/v1/me": ok(ana),
      "GET /api/v1/identities/ana/access": ok({ assignments: [] }),
      "GET /api/v1/identities/ana/eligibilities": () => ({
        status: 200,
        body: { eligibilities: list },
      }),
      "POST /api/v1/eligibilities/el1/activations": (init) => {
        sent = body(init);
        const active = {
          assignment_id: "as1",
          eligibility_id: "el1",
          justification: "INC-9",
          granted_at: "2026-10-16T10:00:00Z",
          expires_at: "2026-10-16T12:00:00Z",
        };
        list = [{ ...eligibility, active }];
        return { status: 201, body: { activation: active } };
      },
    });
    renderAt("/my-access");
    expect(await screen.findByText("Org admins")).toBeInTheDocument();
    expect(screen.getByText(/up to 2 hours/)).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Activate" }));
    const hours = screen.getByLabelText("Hours");
    expect(
      within(hours)
        .getAllByRole("option")
        .map((o) => o.textContent),
    ).toEqual(["1", "2"]);
    await userEvent.selectOptions(hours, "2");
    const go = screen.getByRole("button", { name: "Activate" });
    expect(go).toBeDisabled();
    await userEvent.type(screen.getByLabelText("Why you need it"), "INC-9");
    await userEvent.click(go);
    expect(await screen.findByText(/Active until/)).toBeInTheDocument();
    expect(sent).toEqual({ hours: 2, justification: "INC-9" });
    expect(screen.getByRole("button", { name: "Done, end it now" })).toBeInTheDocument();
  });

  it("says when an activation went for approval", async () => {
    serve({
      "GET /api/v1/me": ok(ana),
      "GET /api/v1/identities/ana/access": ok({ assignments: [] }),
      "GET /api/v1/identities/ana/eligibilities": ok({
        eligibilities: [{ ...eligibility, activation_approval: true }],
      }),
      "POST /api/v1/eligibilities/el1/activations": () => ({
        status: 201,
        body: { request: { id: "q1" } },
      }),
    });
    renderAt("/my-access");
    await userEvent.click(await screen.findByRole("button", { name: "Activate" }));
    await userEvent.type(screen.getByLabelText("Why you need it"), "deploy");
    await userEvent.click(screen.getByRole("button", { name: "Ask to activate" }));
    expect(await screen.findByText(/Activation sent for approval/)).toBeInTheDocument();
  });

  it("asks to be eligible for just-in-time access by default, never for good", async () => {
    let sent: unknown;
    serve({
      "GET /api/v1/me": ok(ana),
      "GET /api/v1/requestable-access": ok({
        items: [
          {
            type: "entitlement",
            id: "e1",
            name: "Org admins",
            application: "GitHub",
            jit_only: true,
          },
        ],
      }),
      "GET /api/v1/identities/ana/access": ok({ assignments: [] }),
      "GET /api/v1/identities/ana/eligibilities": ok({ eligibilities: [] }),
      "GET /api/v1/access-requests?scope=mine&limit=200": ok({ requests: [] }),
      "POST /api/v1/access-requests/preview": ok({
        sod_conflicts: [],
        plan: {
          risk: "high",
          steps: [
            {
              kind: "owner",
              approvers: [{ id: "adm", name: "Ada Admin" }],
              required: 1,
              on_timeout: "expire",
            },
          ],
        },
      }),
      "POST /api/v1/access-requests": (init) => {
        sent = body(init);
        return { status: 201, body: { target_name: "Org admins" } };
      },
    });
    renderAt("/request");
    expect(await screen.findByText("Just in time")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Request Org admins" }));
    expect(screen.getByLabelText(/To be eligible/)).toBeChecked();
    await userEvent.click(screen.getByLabelText(/The access itself/));
    const options = within(screen.getByLabelText("For how long?")).getAllByRole("option");
    expect(options.map((o) => o.textContent)).not.toContain("Until it is removed");
    expect(options[0]).toHaveTextContent("1 hour");
    await userEvent.click(screen.getByLabelText(/To be eligible/));
    await userEvent.selectOptions(screen.getByLabelText("Eligible for how long?"), "d:90");
    await userEvent.type(screen.getByLabelText("Why do you need it?"), "on call");
    await userEvent.click(screen.getByRole("button", { name: "Send request" }));
    await waitFor(() =>
      expect(sent).toEqual({
        entitlement_id: "e1",
        kind: "eligibility",
        justification: "on call",
        duration_days: 90,
      }),
    );
  });

  it("tells approvers it is an activation and for how many hours", async () => {
    const request: AccessRequest = {
      id: "q1",
      requester_id: "ana",
      requester_name: "Ana Núñez",
      beneficiary_id: "ana",
      beneficiary_name: "Ana Núñez",
      type: "entitlement",
      target_id: "e1",
      target_name: "Org admins",
      application: "GitHub",
      justification: "INC-9",
      kind: "activation",
      duration_hours: 2,
      status: "pending",
      created_at: "2026-10-16T09:00:00Z",
      steps: [step({ position: 1, kind: "owner", status: "pending" }, ["adm", "Ada Admin"])],
    };
    serve({
      "GET /api/v1/me": ok(admin),
      "GET /api/v1/access-requests?scope=approvals&limit=200": ok({ requests: [request] }),
    });
    renderAt("/approvals");
    expect(await screen.findByText("Ana Núñez wants to activate Org admins")).toBeInTheDocument();
    expect(screen.getByText(/for 2 hours/)).toBeInTheDocument();
  });

  it("turns standing privileged access into eligibility", async () => {
    let converted = false;
    const standing: Standing[] = [
      {
        assignment_id: "as1",
        identity_id: "luis",
        identity_name: "Luis Pérez",
        type: "entitlement",
        target_id: "e1",
        name: "Org admins",
        application: "GitHub",
        risk: "critical",
        jit_only: true,
        origin: "manual",
        granted_by: "identity:adm",
        granted_at: "2026-01-01T00:00:00Z",
        convertible: true,
      },
      {
        assignment_id: "as2",
        identity_id: "marta",
        identity_name: "Marta Ruiz",
        type: "entitlement",
        target_id: "e2",
        name: "Payments",
        application: "ERP",
        risk: "high",
        jit_only: false,
        origin: "birthright",
        rule_name: "Finance",
        granted_by: "rule",
        granted_at: "2026-01-01T00:00:00Z",
        convertible: false,
      },
    ];
    serve({
      "GET /api/v1/me": ok(admin),
      "GET /api/v1/standing-access?limit=200": ok({ standing }),
      "POST /api/v1/assignments/as1/eligibility": () => {
        converted = true;
        return { status: 201, body: eligibility };
      },
    });
    renderAt("/governance/standing-access");
    const luis = (await screen.findByText("Luis Pérez")).closest("tr")!;
    expect(
      within(screen.getByText("Marta Ruiz").closest("tr")!).getByText("Change the rule"),
    ).toBeInTheDocument();
    await userEvent.click(within(luis).getByRole("button", { name: "Make it just in time" }));
    await waitFor(() => expect(converted).toBe(true));
  });
});

const glassAccount: VaultAccount = {
  id: "v1",
  name: "emergency root",
  username: "root",
  kind: "password",
  entitlement_id: "e1",
  entitlement_name: "Use emergency root",
  risk: "critical",
  checkout_minutes: 240,
  exclusive: true,
  rotate_after_checkin: false,
  can_rotate: true,
  rotation_failures: 0,
  version: 1,
  checkouts: [],
  break_glass: true,
  held: false,
  responder: true,
  created_at: "2026-10-16T10:00:00Z",
};

const glassUse = (id: string, who: string): VaultCheckout => ({
  id,
  vault_account_id: "v1",
  identity_id: who,
  identity_name: who === "adm" ? "Admin" : "Ana Núñez",
  version: 1,
  reason: "INC-9 production down",
  checked_out_at: "2026-10-16T10:00:00Z",
  expires_at: "2026-10-16T11:00:00Z",
  account_name: "emergency root",
  break_glass: true,
  review_status: "pending",
});

describe("Break glass", () => {
  it("lets a responder open the account in an emergency, with a warning", async () => {
    let sent: unknown;
    serve({
      "GET /api/v1/me": ok(ana),
      "GET /api/v1/vault/accounts": ok({ enabled: true, accounts: [glassAccount] }),
      "POST /api/v1/vault/accounts/v1/break-glass": (init) => {
        sent = body(init);
        return { status: 201, body: { ...glassUse("c1", "ana"), secret: "glass-pw" } };
      },
    });
    renderAt("/vault");
    expect(await screen.findByText("emergency root")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Check out" })).not.toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Break glass" }));
    expect(screen.getByText(/Every admin and auditor is told at once/)).toBeInTheDocument();
    await userEvent.type(screen.getByLabelText("What is the emergency?"), "INC-9");
    await userEvent.click(screen.getByRole("button", { name: "Break glass" }));
    expect(await screen.findByDisplayValue("glass-pw")).toBeInTheDocument();
    expect(sent).toEqual({ reason: "INC-9" });
    expect(screen.getByText(/changes this password once nobody has it/)).toBeInTheDocument();
  });

  it("has another admin review each use, flagging only with a reason", async () => {
    let sent: unknown;
    serve({
      "GET /api/v1/me": ok(admin),
      "GET /api/v1/vault/accounts": ok({ enabled: true, accounts: [glassAccount] }),
      "GET /api/v1/vault/break-glass?status=pending": ok({
        uses: [glassUse("c1", "ana"), glassUse("c2", "adm")],
      }),
      "POST /api/v1/vault/break-glass/c1/review": (init) => {
        sent = body(init);
        return { status: 200, body: { ...glassUse("c1", "ana"), review_status: "flagged" } };
      },
    });
    renderAt("/vault?tab=break-glass");
    const items = await screen.findAllByRole("listitem");
    const anas = items.find((li) => li.textContent?.includes("Ana Núñez opened"))!;
    const mine = items.find((li) => li.textContent?.includes("Admin opened"))!;
    expect(within(mine).queryByRole("button", { name: "Justified" })).not.toBeInTheDocument();
    const flag = within(anas).getByRole("button", { name: "Flag it" });
    expect(flag).toBeDisabled();
    await userEvent.type(within(anas).getByLabelText(/Comment/), "no such incident");
    await userEvent.click(flag);
    await waitFor(() => expect(sent).toEqual({ justified: false, comment: "no such incident" }));
  });
});
