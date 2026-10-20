import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import type { AuditEvent, Identity, Me, PlatformRoleInfo } from "@/lib/api";
import { ok, renderAt, serve } from "@/test-utils";

const admin: Me = {
  identity_id: "ada",
  display_name: "Ada Admin",
  platform_roles: ["grantra-admin"],
  manages_people: false,
  owns_applications: false,
};
const helpdesk: Me = { ...admin, identity_id: "hugo", platform_roles: ["grantra-helpdesk"] };

const person = (id: string, name: string, extra: Partial<Identity> = {}): Identity => ({
  id,
  display_name: name,
  email: `${id}@example.com`,
  kind: "human",
  status: "active",
  attributes: {},
  created_at: "2026-01-10T09:00:00Z",
  ...extra,
});

const pedro = person("pedro", "Pedro Gil", {
  source_id: "hr",
  external_id: "E042",
  manager_id: "marta",
  start_date: "2026-02-01",
  attributes: { department: "Finance" },
});
const marta = person("marta", "Marta Ruiz");

const roles: PlatformRoleInfo[] = [
  {
    name: "grantra-helpdesk",
    display_name: "Grantra helpdesk",
    description: "Looks people up.",
    risk: "medium",
    members: [],
  },
];

const personRoutes = {
  "GET /api/v1/identities/pedro": ok(pedro),
  "GET /api/v1/identities/marta": ok(marta),
  "GET /api/v1/identities/pedro/access": ok({ assignments: [] }),
};

describe("People", () => {
  it("searches people by name and links to each one", async () => {
    const fetch = serve({
      "GET /api/v1/me": ok(helpdesk),
      "GET /api/v1/identities?limit=50": ok({ identities: [pedro, marta] }),
      "GET /api/v1/identities?limit=50&q=marta": ok({ identities: [marta] }),
    });
    renderAt("/people");

    expect(await screen.findByRole("link", { name: "Pedro Gil" })).toHaveAttribute(
      "href",
      "/people/pedro",
    );
    expect(screen.getByText("HR, employee E042")).toBeInTheDocument();

    await userEvent.type(screen.getByRole("searchbox"), "marta");
    await waitFor(() => expect(screen.queryByText("Pedro Gil")).not.toBeInTheDocument());
    expect(screen.getByRole("link", { name: "Marta Ruiz" })).toBeInTheDocument();
    expect(fetch).toHaveBeenCalledWith("/api/v1/identities?limit=50&q=marta", expect.anything());
  });

  it("shows helpdesk a person's details and access, without roles or history", async () => {
    serve({ "GET /api/v1/me": ok(helpdesk), ...personRoutes });
    renderAt("/people/pedro");

    expect(await screen.findByRole("heading", { name: "Pedro Gil" })).toBeInTheDocument();
    expect(await screen.findByRole("link", { name: "Marta Ruiz" })).toHaveAttribute(
      "href",
      "/people/marta",
    );
    expect(screen.getByText("Department")).toBeInTheDocument();
    expect(screen.getByText("Finance")).toBeInTheDocument();
    expect(await screen.findByText("No access through Grantra.")).toBeInTheDocument();
    expect(screen.queryByText("Grantra roles")).not.toBeInTheDocument();
    expect(screen.queryByRole("tab", { name: "History" })).not.toBeInTheDocument();
  });

  it("lets an admin give a Grantra role", async () => {
    let granted: unknown;
    let members = roles[0]!.members;
    serve({
      "GET /api/v1/me": ok(admin),
      ...personRoutes,
      "GET /api/v1/platform-roles": () => ({
        status: 200,
        body: { roles: [{ ...roles[0]!, members }] },
      }),
      "PUT /api/v1/platform-roles/grantra-helpdesk/members/pedro": (init) => {
        granted = JSON.parse(init!.body as string);
        members = [
          {
            role: "grantra-helpdesk",
            identity_id: "pedro",
            display_name: "Pedro Gil",
            status: "active",
            granted_at: "2026-10-13T09:00:00Z",
          },
        ];
        return { status: 204, body: null };
      },
    });
    vi.spyOn(window, "confirm").mockReturnValue(true);
    renderAt("/people/pedro");

    await userEvent.click(await screen.findByRole("button", { name: "Give with no end date" }));
    expect(window.confirm).toHaveBeenCalledWith("Give Grantra helpdesk to Pedro Gil?");
    expect(await screen.findByText("Has it")).toBeInTheDocument();
    expect(granted).toEqual({});
    expect(screen.getByRole("button", { name: "Remove" })).toBeInTheDocument();
  });

  it("does not offer admins a Grantra role for themselves", async () => {
    serve({
      "GET /api/v1/me": ok({ ...admin, identity_id: "pedro" }),
      ...personRoutes,
      "GET /api/v1/platform-roles": ok({ roles }),
    });
    renderAt("/people/pedro");

    expect(
      await screen.findByText("Nobody can give a Grantra role to themselves."),
    ).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /Give/ })).not.toBeInTheDocument();
  });
});

describe("Audit log", () => {
  const event = (id: number, extra: Partial<AuditEvent>): AuditEvent => ({
    id,
    occurred_at: "2026-10-13T09:00:00Z",
    actor: "identity:ada",
    actor_name: "Ada Admin",
    action: "platform_role.granted",
    target_type: "identity",
    target_id: "pedro",
    target_name: "Pedro Gil",
    details: { role: "grantra-helpdesk" },
    ...extra,
  });

  it("lists events in words and filters them", async () => {
    serve({
      "GET /api/v1/me": ok(admin),
      "GET /api/v1/audit-events?limit=100": ok({
        events: [
          event(2, {}),
          event(1, {
            actor: "system:lifecycle",
            actor_name: undefined,
            action: "identity_source.synced",
            target_type: "identity_source",
            target_id: "hr",
            target_name: undefined,
            details: {},
          }),
        ],
      }),
    });
    renderAt("/audit");

    const rows = await screen.findAllByRole("row");
    expect(within(rows[1]!).getByRole("link", { name: "Ada Admin" })).toHaveAttribute(
      "href",
      "/people/ada",
    );
    expect(within(rows[1]!).getByText("Platform role granted")).toBeInTheDocument();
    expect(within(rows[2]!).getByText("Grantra (HR sync)")).toBeInTheDocument();

    await userEvent.type(screen.getByRole("searchbox"), "synced");
    expect(screen.getAllByRole("row")).toHaveLength(2);
    expect(screen.getByText("1 of 2 loaded events")).toBeInTheDocument();
  });
});
