import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";
import type { Me } from "@/lib/api";
import type { Framework, ReportDefinition, ReportResult } from "@/lib/reports";
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
      "GET /api/v1/me/inbox": ok({
        approvals: [],
        reviews: [],
        break_glass: [],
        threats: [],
        total: 0,
      }),
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
});
