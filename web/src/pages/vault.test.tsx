import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";
import { PlatformRole, type Me } from "@/lib/api";
import type { VaultAccount, VaultFinding } from "@/lib/vault";
import { ok, renderAt, serve } from "@/test-utils";

const ana: Me = {
  identity_id: "ana",
  display_name: "Ana Núñez",
  platform_roles: [],
  manages_people: false,
  owns_applications: false,
};
const admin: Me = { ...ana, identity_id: "adm", platform_roles: [PlatformRole.Admin] };

const rootAccount: VaultAccount = {
  id: "v1",
  name: "prod-db root",
  username: "root",
  address: "prod-db:5432",
  kind: "password",
  entitlement_id: "e1",
  entitlement_name: "Use prod-db root",
  risk: "critical",
  checkout_minutes: 60,
  exclusive: false,
  rotate_after_checkin: true,
  can_rotate: true,
  rotation_failures: 0,
  version: 2,
  secret_at: "2026-10-16T10:00:00Z",
  checkouts: [],
  held: true,
  created_at: "2026-10-16T10:00:00Z",
};

describe("Vault", () => {
  it("checks out a password with a reason and checks it back in", async () => {
    const fetch = serve({
      "GET /api/v1/me": ok(ana),
      "GET /api/v1/vault/accounts": ok({ enabled: true, accounts: [rootAccount] }),
      "POST /api/v1/vault/accounts/v1/checkouts": () => ({
        status: 201,
        body: {
          id: "c1",
          vault_account_id: "v1",
          identity_id: "ana",
          identity_name: "Ana Núñez",
          version: 2,
          reason: "INC-7",
          checked_out_at: "2026-10-16T11:00:00Z",
          expires_at: "2026-10-16T12:00:00Z",
          secret: "Tr0ub4dor&3",
        },
      }),
      "POST /api/v1/vault/checkouts/c1/checkin": () => ({ status: 204, body: null }),
    });
    renderAt("/vault");
    expect(await screen.findByText("prod-db root")).toBeInTheDocument();
    expect(screen.queryByRole("tab", { name: "Discovery" })).not.toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Check out" }));
    await userEvent.type(screen.getByLabelText(/Why do you need/), "INC-7");
    await userEvent.click(screen.getByRole("button", { name: "Check out" }));
    expect(await screen.findByDisplayValue("Tr0ub4dor&3")).toBeInTheDocument();
    const post = fetch.mock.calls.find(([path]) => path === "/api/v1/vault/accounts/v1/checkouts");
    expect(post?.[1]?.body).toBe(JSON.stringify({ reason: "INC-7" }));
    expect(screen.getByText(/changes this password once nobody has it/)).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: /Check in/ }));
    await waitFor(() => expect(screen.queryByDisplayValue("Tr0ub4dor&3")).not.toBeInTheDocument());
  });

  it("tells people without vault access how to get it", async () => {
    serve({
      "GET /api/v1/me": ok(ana),
      "GET /api/v1/vault/accounts": ok({ enabled: false, accounts: [] }),
    });
    renderAt("/vault");
    expect(await screen.findByText(/You hold no vault access/)).toBeInTheDocument();
    expect(screen.getByText(/GRANTRA_VAULT_KEY/)).toBeInTheDocument();
  });

  it("adds a discovered account to the vault", async () => {
    const finding: VaultFinding = {
      account_id: "a1",
      application_id: "app",
      application_name: "AD",
      username: "svc-backup",
      native_id: "guid",
      reasons: ["privileged", "orphan"],
      entitlements: ["Domain Admins"],
      can_rotate: true,
    };
    const fetch = serve({
      "GET /api/v1/me": ok(admin),
      "GET /api/v1/vault/accounts": ok({
        enabled: true,
        accounts: [{ ...rootAccount, held: false }],
      }),
      "GET /api/v1/vault/discovery?limit=200": ok({ findings: [finding] }),
      "POST /api/v1/vault/accounts": () => ({ status: 201, body: rootAccount }),
    });
    renderAt("/vault");
    // An admin who does not hold the access cannot check it out.
    expect(await screen.findByText("prod-db root")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Check out" })).not.toBeInTheDocument();

    await userEvent.click(screen.getByRole("tab", { name: "Discovery" }));
    const row = (await screen.findByText("svc-backup")).closest("tr")!;
    expect(within(row).getByText("Privileged access")).toBeInTheDocument();
    expect(within(row).getByText("Domain Admins")).toBeInTheDocument();
    await userEvent.click(within(row).getByRole("button", { name: "Add to vault" }));
    expect(screen.getByLabelText("Name")).toHaveValue("AD svc-backup");
    await userEvent.type(screen.getByLabelText(/Rotate every/), "{selectall}30");
    await userEvent.click(screen.getByRole("button", { name: "Add" }));
    await waitFor(() =>
      expect(fetch.mock.calls.some(([, init]) => init?.method === "POST")).toBe(true),
    );
    const body = JSON.parse(
      fetch.mock.calls.find(([, init]) => init?.method === "POST")![1]!.body as string,
    ) as Record<string, unknown>;
    expect(body).toMatchObject({ account_id: "a1", name: "AD svc-backup", rotate_every_days: 30 });
  });
});
