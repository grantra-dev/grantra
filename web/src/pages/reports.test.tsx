import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";
import type { Me } from "@/lib/api";
import type {
  ExternalAuditor,
  Framework,
  ReportDefinition,
  ReportResult,
  Schedule,
} from "@/lib/reports";
import { adminMe, ok, renderAt, serve } from "@/test-utils";

const audrey: Me = { ...adminMe, identity_id: "audrey", platform_roles: ["grantra-auditor"] };

const leavers: ReportDefinition = {
  id: "leavers",
  title: "Leavers and orphan accounts",
  description: "Everyone who left in the period.",
  period: true,
  application: true,
  tables: [
    {
      name: "leavers",
      title: "Leavers",
      columns: [
        { key: "person", title: "Person" },
        { key: "target", title: "Within target" },
      ],
    },
  ],
};
const access: ReportDefinition = {
  id: "access",
  title: "Who has access to what",
  description: "Everything everyone holds now.",
  period: false,
  application: true,
  tables: [],
};

const soc2: Framework = {
  id: "soc2",
  name: "SOC 2",
  description: "Trust Services Criteria.",
  builtin: true,
  customized: false,
  enabled: true,
  controls: [{ id: "CC6.2", title: "Users are registered and removed", reports: ["leavers"] }],
};

const inbox = ok({ approvals: [], reviews: [], break_glass: [], threats: [], total: 0 });

const quarterly: Schedule = {
  id: "s1",
  name: "SOC 2 for the auditors",
  framework: "soc2",
  months: 3,
  recipients: [
    { id: "audrey", name: "Audrey", allowed: true },
    { id: "gone", name: "Gonzalo", allowed: false },
  ],
  enabled: true,
  updated_by: "Admin",
  updated_at: "2026-10-01T00:00:00Z",
  next_from: "2026-10-01",
  next_to: "2026-12-31",
  last: {
    id: "d1",
    title: "SOC 2",
    from: "2026-07-01",
    to: "2026-09-30",
    status: "ready",
    bytes: 4096,
    recipients: 1,
    created_at: "2026-10-01T00:05:00Z",
  },
};

const ines: ExternalAuditor = {
  id: "ines",
  name: "Ines Ruiz",
  email: "ines@audit.example",
  company: "Audit & Co",
  until: "2026-11-30",
  status: "active",
  sponsor: "Admin",
  invited_at: "2026-10-01T00:00:00Z",
};

const reportList = ok({
  reports: [leavers, access],
  default_from: "2026-07-01",
  default_to: "2026-09-30",
});

describe("Reports", () => {
  it("lists reports with the controls they cover, read-only for auditors", async () => {
    serve({
      "GET /api/v1/me": ok(audrey),
      "GET /api/v1/reports": reportList,
      "GET /api/v1/report-frameworks": ok({ frameworks: [soc2] }),
    });
    renderAt("/governance/reports");
    const card = await screen.findByRole("link", { name: /Leavers and orphan accounts/ });
    expect(card).toHaveTextContent("For a period");
    await waitFor(() => expect(card).toHaveTextContent("Evidence for SOC 2: CC6.2"));
    expect(screen.getByRole("link", { name: /Who has access to what/ })).toHaveTextContent(
      "As of now",
    );

    await userEvent.click(screen.getByRole("tab", { name: "Frameworks" }));
    expect(await screen.findByText("Users are registered and removed")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: /Evidence pack/ })).toHaveAttribute(
      "href",
      "/api/v1/report-frameworks/soc2/pack?from=2026-07-01&to=2026-09-30",
    );
    expect(screen.queryByRole("button", { name: "Edit" })).not.toBeInTheDocument();
  });

  it("runs a report and offers every download for the period picked", async () => {
    const result: ReportResult = {
      report: leavers,
      params: { from: "2026-07-01T00:00:00Z", to: "2026-09-30T00:00:00Z" },
      generated_at: "2026-10-23T08:00:00Z",
      facts: [{ label: "Closed late or still open", value: 1 }],
      tables: [{ ...leavers.tables[0]!, rows: [["Bea", "no"]], row_count: 250 }],
    };
    serve({
      "GET /api/v1/me": ok(audrey),
      "GET /api/v1/reports": reportList,
      "GET /api/v1/applications": ok({ applications: [] }),
      "GET /api/v1/reports/leavers?from=2026-07-01&to=2026-09-30": ok(result),
    });
    renderAt("/governance/reports/leavers");
    expect(await screen.findByText("Closed late or still open")).toBeInTheDocument();
    const row = screen.getByRole("row", { name: /Bea/ });
    expect(row).toHaveTextContent("no");
    expect(screen.getByText(/Showing the first 1 of 250/)).toBeInTheDocument();
    expect(screen.getByRole("link", { name: /Evidence pack/ })).toHaveAttribute(
      "href",
      "/api/v1/reports/leavers?from=2026-07-01&to=2026-09-30&format=zip",
    );
    expect(screen.getByRole("link", { name: /CSV/ })).toHaveAttribute(
      "href",
      "/api/v1/reports/leavers?from=2026-07-01&to=2026-09-30&format=csv&table=leavers",
    );
    expect(screen.getByRole("link", { name: /Printable summary/ })).toHaveAttribute(
      "href",
      "/api/v1/reports/leavers?from=2026-07-01&to=2026-09-30&format=html",
    );
  });

  it("lets an admin map a control to other reports", async () => {
    let saved: unknown;
    serve({
      "GET /api/v1/me": ok(adminMe),
      "GET /api/v1/me/inbox": inbox,
      "GET /api/v1/reports": reportList,
      "GET /api/v1/report-frameworks": ok({ frameworks: [soc2] }),
      "PUT /api/v1/report-frameworks/soc2": (init) => {
        saved = JSON.parse(init?.body as string);
        return { status: 200, body: { ...soc2, customized: true } };
      },
    });
    renderAt("/governance/reports?tab=frameworks");
    await userEvent.click(await screen.findByRole("button", { name: "Edit" }));
    const form = screen.getByRole("heading", { name: "Edit SOC 2" }).closest("form")!;
    expect(within(form).queryByLabelText("Name")).not.toBeInTheDocument();
    const id = within(form).getByLabelText("Control 1 id");
    await userEvent.clear(id);
    await userEvent.type(id, "ITGC-02");
    await userEvent.click(within(form).getByLabelText("Who has access to what"));
    await userEvent.click(within(form).getByRole("button", { name: "Save" }));
    await waitFor(() =>
      expect(saved).toEqual({
        ...soc2,
        controls: [
          {
            id: "ITGC-02",
            title: "Users are registered and removed",
            reports: ["leavers", "access"],
          },
        ],
      }),
    );
  });

  it("says whether a downloaded file is unchanged", async () => {
    serve({
      "GET /api/v1/me": ok(audrey),
      "GET /api/v1/reports": reportList,
      "POST /api/v1/report-verifications": ok({
        valid: false,
        pack: true,
        sha256: "abc",
        problems: ["leavers/leavers.csv was changed"],
        manifest: {
          title: "SOC 2",
          from: "2026-07-01",
          to: "2026-09-30",
          generated_at: "2026-10-23T08:00:00Z",
          generated_by: { actor: "identity:audrey" },
          files: [],
        },
      }),
    });
    renderAt("/governance/reports?tab=verify");
    await userEvent.upload(
      await screen.findByLabelText("File to verify"),
      new File(["PK"], "pack.zip", { type: "application/zip" }),
    );
    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent("does not match");
    expect(alert).toHaveTextContent("leavers/leavers.csv was changed");
    expect(alert).toHaveTextContent("SOC 2, 2026-07-01 to 2026-09-30");
  });

  it("shows scheduled packs to auditors, with their downloads", async () => {
    serve({
      "GET /api/v1/me": ok(audrey),
      "GET /api/v1/reports": reportList,
      "GET /api/v1/report-frameworks": ok({ frameworks: [soc2] }),
      "GET /api/v1/report-schedules": ok({ schedules: [quarterly] }),
      "GET /api/v1/report-deliveries": ok({ deliveries: [quarterly.last] }),
    });
    renderAt("/governance/reports?tab=scheduled");
    const section = (await screen.findByRole("heading", { name: quarterly.name })).closest(
      "section",
    )!;
    expect(section).toHaveTextContent("SOC 2 · Every quarter · next: 2026-10-01 to 2026-12-31");
    expect(section).toHaveTextContent("no longer an admin or auditor");
    expect(within(section).getByRole("link", { name: /Download/ })).toHaveAttribute(
      "href",
      "/api/v1/report-deliveries/d1/file",
    );
    expect(await screen.findByRole("link", { name: /4 KB/ })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Edit" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Schedule a pack" })).not.toBeInTheDocument();
  });

  it("lets an admin schedule a framework's pack for admins and auditors", async () => {
    let saved: unknown;
    serve({
      "GET /api/v1/me": ok(adminMe),
      "GET /api/v1/me/inbox": inbox,
      "GET /api/v1/reports": reportList,
      "GET /api/v1/report-frameworks": ok({ frameworks: [soc2] }),
      "GET /api/v1/report-schedules": ok({ schedules: [] }),
      "GET /api/v1/report-deliveries": ok({ deliveries: [] }),
      "GET /api/v1/platform-roles": ok({
        roles: [
          {
            name: "grantra-auditor",
            display_name: "Auditor",
            description: "",
            risk: "high",
            members: [
              {
                role: "grantra-auditor",
                identity_id: "audrey",
                display_name: "Audrey",
                status: "active",
                granted_at: "2026-01-01T00:00:00Z",
              },
            ],
          },
          {
            name: "grantra-helpdesk",
            display_name: "Helpdesk",
            description: "",
            risk: "medium",
            members: [
              {
                role: "grantra-helpdesk",
                identity_id: "hal",
                display_name: "Hal",
                status: "active",
                granted_at: "2026-01-01T00:00:00Z",
              },
            ],
          },
        ],
      }),
      "POST /api/v1/report-schedules": (init) => {
        saved = JSON.parse(init?.body as string);
        return { status: 201, body: quarterly };
      },
    });
    renderAt("/governance/reports?tab=scheduled");
    await userEvent.click(await screen.findByRole("button", { name: "Schedule a pack" }));
    const form = screen.getByRole("heading", { name: "Schedule a pack" }).closest("form")!;
    await userEvent.type(within(form).getByLabelText("Name"), "SOC 2 for the auditors");
    await userEvent.selectOptions(within(form).getByLabelText("What"), "framework:soc2");
    expect(within(form).queryByLabelText("Application")).not.toBeInTheDocument();
    // Only admins and auditors can get packs.
    expect(within(form).queryByLabelText("Hal")).not.toBeInTheDocument();
    await userEvent.click(within(form).getByLabelText("Audrey"));
    await userEvent.click(within(form).getByRole("button", { name: "Schedule it" }));
    await waitFor(() =>
      expect(saved).toEqual({
        name: "SOC 2 for the auditors",
        report: "",
        framework: "soc2",
        application_id: "",
        months: 3,
        recipients: ["audrey"],
        enabled: true,
      }),
    );
  });

  it("invites an external auditor until a date, and ends it early", async () => {
    let invited: unknown;
    let ended = false;
    serve({
      "GET /api/v1/me": ok(adminMe),
      "GET /api/v1/me/inbox": inbox,
      "GET /api/v1/reports": reportList,
      "GET /api/v1/external-auditors": ok({ auditors: [ines] }),
      "POST /api/v1/external-auditors": (init) => {
        invited = JSON.parse(init?.body as string);
        return {
          status: 201,
          body: { ...ines, id: "joe", name: "Joe", login_error: "Grantra Login is not set up" },
        };
      },
      "DELETE /api/v1/external-auditors/ines": () => {
        ended = true;
        return { status: 204, body: null };
      },
    });
    renderAt("/governance/reports?tab=auditors");
    const row = await screen.findByRole("row", { name: /Ines Ruiz/ });
    expect(row).toHaveTextContent("ines@audit.example · Audit & Co");
    expect(within(row).getByLabelText("Last day of Ines Ruiz")).toHaveValue("2026-11-30");

    await userEvent.click(screen.getByRole("button", { name: "Invite an auditor" }));
    await userEvent.type(screen.getByLabelText("Name"), "Joe");
    await userEvent.type(screen.getByLabelText("Email"), "joe@audit.example");
    const until = screen.getByLabelText("Last day");
    await userEvent.clear(until);
    await userEvent.type(until, "2026-12-15");
    await userEvent.click(screen.getByRole("button", { name: "Invite" }));
    expect(await screen.findByText(/Joe is invited. No Grantra Login account/)).toBeInTheDocument();
    expect(invited).toEqual({
      name: "Joe",
      email: "joe@audit.example",
      company: "",
      until: "2026-12-15",
      login: true,
    });

    await userEvent.click(within(row).getByRole("button", { name: "End now" }));
    await waitFor(() => expect(ended).toBe(true));
  });
});
