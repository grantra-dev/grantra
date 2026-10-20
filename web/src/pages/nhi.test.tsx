import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";
import type { Identity, Me } from "@/lib/api";
import type { Candidate, NonHuman } from "@/lib/nhi";
import { ok, renderAt, serve } from "@/test-utils";

const ada: Me = {
  identity_id: "ada",
  display_name: "Ada Admin",
  platform_roles: ["grantra-admin"],
  manages_people: false,
  owns_applications: false,
};

const backup: NonHuman = {
  id: "n1",
  kind: "service",
  name: "backup-sync",
  purpose: "Copies the file server to the cloud",
  status: "active",
  owner_id: "pedro",
  owner_name: "Pedro",
  owner_status: "active",
  created_at: "2026-01-01T09:00:00Z",
  accounts: 1,
  vaulted: 0,
  risky_held: 1,
  last_used_at: "2026-10-18T09:00:00Z",
  flags: ["unvaulted"],
};

const orphanBot: NonHuman = {
  ...backup,
  id: "n2",
  kind: "agent",
  name: "old-bot",
  purpose: undefined,
  owner_id: undefined,
  owner_name: undefined,
  owner_status: undefined,
  accounts: 0,
  risky_held: 0,
  flags: ["no_owner"],
};

const summary = { total: 2, no_owner: 1, unvaulted: 1, unused: 0, expiring: 0 };

const pedro: Identity = {
  id: "pedro",
  display_name: "Pedro",
  kind: "human",
  status: "active",
  attributes: {},
  created_at: "2025-01-01T09:00:00Z",
};

describe("Service accounts and agents", () => {
  it("lists them with what needs a look, and filters by it", async () => {
    const fetch = serve({
      "GET /api/v1/me": ok(ada),
      "GET /api/v1/non-human-identities/summary": ok(summary),
      "GET /api/v1/non-human-identities?limit=50": ok({ identities: [backup, orphanBot] }),
      "GET /api/v1/non-human-identities?limit=50&flag=no_owner": ok({ identities: [orphanBot] }),
    });
    renderAt("/governance/non-human-identities");
    const row = (await screen.findByRole("link", { name: "backup-sync" })).closest("tr")!;
    expect(within(row).getByText("Secret outside the vault")).toBeInTheDocument();
    expect(within(row).getByRole("link", { name: "Pedro" })).toHaveAttribute(
      "href",
      "/people/pedro",
    );
    expect(
      within(screen.getByRole("link", { name: "old-bot" }).closest("tr")!).getByText("Nobody"),
    );
    await userEvent.click(screen.getByRole("button", { name: "No owner (1)" }));
    await waitFor(() => expect(screen.queryByRole("link", { name: "backup-sync" })).toBeNull());
    expect(fetch).toHaveBeenCalledWith(
      "/api/v1/non-human-identities?limit=50&flag=no_owner",
      expect.anything(),
    );
  });

  it("adds one with its owner", async () => {
    const fetch = serve({
      "GET /api/v1/me": ok(ada),
      "GET /api/v1/non-human-identities/summary": ok({ ...summary, total: 0 }),
      "GET /api/v1/non-human-identities?limit=50": ok({ identities: [] }),
      "GET /api/v1/identities?limit=50&q=pe&status=active&kind=human": ok({ identities: [pedro] }),
      "POST /api/v1/non-human-identities": ok(backup),
    });
    renderAt("/governance/non-human-identities");
    await userEvent.click(await screen.findByRole("button", { name: "Add one" }));
    await userEvent.selectOptions(
      screen.getByLabelText("Kind", { selector: "#nhi-new-kind-new" }),
      "agent",
    );
    await userEvent.type(screen.getByLabelText("Name"), "support-bot");
    await userEvent.type(screen.getByLabelText("Owner"), "pe");
    await userEvent.click(await screen.findByRole("button", { name: "Pick Pedro" }));
    await userEvent.type(screen.getByLabelText("What it is for"), "Answers tickets");
    await userEvent.click(screen.getByRole("button", { name: "Add it" }));
    await waitFor(() =>
      expect(
        fetch.mock.calls.some(
          ([p, i]) => p === "/api/v1/non-human-identities" && i?.method === "POST",
        ),
      ).toBe(true),
    );
    const call = fetch.mock.calls.find(
      ([p, i]) => p === "/api/v1/non-human-identities" && i?.method === "POST",
    )!;
    expect(JSON.parse(call[1]!.body as string)).toMatchObject({
      kind: "agent",
      name: "support-bot",
      owner_id: "pedro",
      purpose: "Answers tickets",
      account_ids: [],
    });
  });

  it("gives an account nobody owns an owner", async () => {
    const svc: Candidate = {
      account_id: "a1",
      username: "svc-backup",
      application_id: "ad",
      application: "Active Directory",
      entitlements: 2,
      risky: true,
      vaulted: false,
      looks_like_service: true,
    };
    const fetch = serve({
      "GET /api/v1/me": ok(ada),
      "GET /api/v1/non-human-identities/candidates": ok({ accounts: [svc], limit: 500 }),
      "GET /api/v1/identities?limit=50&q=pe&status=active&kind=human": ok({ identities: [pedro] }),
      "POST /api/v1/non-human-identities": ok(backup),
    });
    renderAt("/governance/non-human-identities?tab=unowned");
    expect(await screen.findByText("Looks like a program")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Give it an owner" }));
    expect(screen.getByText("Who answers for svc-backup?")).toBeInTheDocument();
    expect(screen.getByLabelText("Name")).toHaveValue("svc-backup");
    await userEvent.type(screen.getByLabelText("Owner"), "pe");
    await userEvent.click(await screen.findByRole("button", { name: "Pick Pedro" }));
    await userEvent.click(screen.getByRole("button", { name: "Add it" }));
    await waitFor(() =>
      expect(
        fetch.mock.calls.some(
          ([p, i]) => p === "/api/v1/non-human-identities" && i?.method === "POST",
        ),
      ).toBe(true),
    );
    const call = fetch.mock.calls.find(
      ([p, i]) => p === "/api/v1/non-human-identities" && i?.method === "POST",
    )!;
    expect(JSON.parse(call[1]!.body as string)).toMatchObject({
      kind: "service",
      owner_id: "pedro",
      account_ids: ["a1"],
    });
  });

  it("shows the owner on its page, and lets an admin retire it", async () => {
    const asIdentity: Identity = {
      id: "n1",
      display_name: "backup-sync",
      kind: "service",
      status: "active",
      manager_id: "pedro",
      purpose: "Copies the file server to the cloud",
      attributes: {},
      created_at: "2026-01-01T09:00:00Z",
    };
    const fetch = serve({
      "GET /api/v1/me": ok(ada),
      "GET /api/v1/identities/n1": ok(asIdentity),
      "GET /api/v1/identities/pedro": ok(pedro),
      "GET /api/v1/non-human-identities/n1": ok(backup),
      "GET /api/v1/identities/n1/access": ok({ assignments: [] }),
      "GET /api/v1/identities/n1/eligibilities": ok({ eligibilities: [] }),
      "POST /api/v1/non-human-identities/n1/retire": ok({ ...backup, status: "terminated" }),
    });
    renderAt("/people/n1");
    expect(
      await screen.findByRole("link", { name: "Service accounts and agents" }),
    ).toHaveAttribute("href", "/governance/non-human-identities");
    expect(await screen.findByRole("link", { name: "Pedro" })).toHaveAttribute(
      "href",
      "/people/pedro",
    );
    expect(screen.getByText("Copies the file server to the cloud")).toBeInTheDocument();
    expect(screen.queryByText("Employee id")).not.toBeInTheDocument();
    expect(await screen.findByText("Secret outside the vault")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Change" }));
    await userEvent.type(screen.getByLabelText("Retire it"), "Replaced");
    await userEvent.click(screen.getByRole("button", { name: "Retire backup-sync" }));
    await waitFor(() =>
      expect(fetch).toHaveBeenCalledWith(
        "/api/v1/non-human-identities/n1/retire",
        expect.anything(),
      ),
    );
  });
});
