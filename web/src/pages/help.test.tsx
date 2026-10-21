import { screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";
import accountsTemplate from "../../public/templates/app-accounts.csv?raw";
import hrTemplate from "../../public/templates/hr-people.csv?raw";
import { helpPages } from "@/help/pages";
import { accountFields, hrFields, type CatalogApplication } from "@/lib/admin";
import type { Me } from "@/lib/api";
import { headingId, parseMarkdown, resolveLink } from "@/lib/markdown";
import { ok, renderAt, serve } from "@/test-utils";

const worker: Me = {
  identity_id: "wanda",
  display_name: "Wanda Worker",
  platform_roles: [],
  manages_people: false,
  owns_applications: false,
};
const admin: Me = { ...worker, display_name: "Ada Admin", platform_roles: ["grantra-admin"] };

describe("Help", () => {
  it("shows each person the pages for what they do", async () => {
    serve({ "GET /api/v1/me": ok(worker) });
    renderAt("/help");
    expect(await screen.findByRole("heading", { name: "Help", level: 1 })).toBeInTheDocument();
    expect(screen.getByRole("link", { name: /Getting started/ })).toBeInTheDocument();
    expect(screen.getByRole("link", { name: /Privileged access/ })).toBeInTheDocument();
    expect(screen.queryByRole("link", { name: /Approvals and reviews/ })).not.toBeInTheDocument();
    expect(screen.queryByRole("link", { name: /CSV files/ })).not.toBeInTheDocument();
  });

  it("keeps admin pages from other people", async () => {
    serve({ "GET /api/v1/me": ok(worker) });
    renderAt("/help/csv-files");
    expect(await screen.findByText("Page not found")).toBeInTheDocument();
  });

  it("renders a page, with templates to download and other docs on GitHub", async () => {
    serve({ "GET /api/v1/me": ok(admin) });
    renderAt("/help/csv-files");
    const page = await screen.findByRole("article", { name: "CSV files" });
    expect(within(page).getByRole("heading", { name: "When a file is refused" })).toHaveAttribute(
      "id",
      "when-a-file-is-refused",
    );
    expect(within(page).getByRole("link", { name: "hr-people.csv" })).toHaveAttribute(
      "href",
      "/templates/hr-people.csv",
    );
    expect(within(page).getByRole("link", { name: "HR source" })).toHaveAttribute(
      "href",
      "https://github.com/grantra-dev/grantra/blob/main/docs/identity-lifecycle.md",
    );
    expect(within(page).getByText(/wrong number of fields/)).toBeInTheDocument();
  });

  it("links between pages in the console", async () => {
    serve({ "GET /api/v1/me": ok(admin) });
    renderAt("/help/administration");
    const page = await screen.findByRole("article", { name: "Administration" });
    expect(within(page).getAllByRole("link", { name: "CSV files" })[0]).toHaveAttribute(
      "href",
      "/help/csv-files",
    );
  });
});

// Files the help pages may link to.
const docs = import.meta.glob("../../../docs/**/*.md", {
  query: "?raw",
  import: "default",
  eager: true,
});
const templates = import.meta.glob("../../public/templates/*", { query: "?raw", eager: true });

describe("Help pages", () => {
  const all = new Set(helpPages.map((p) => p.slug));
  const links = (md: string) => [...md.matchAll(/\]\(([^)\s]+)\)/g)].map((m) => m[1] ?? "");

  it.each(helpPages.map((p) => [p.slug, p] as const))("%s has no broken links", (_, page) => {
    for (const href of links(page.body)) {
      const target = resolveLink(href, all);
      const [file, anchor] = href.split("#");
      if (target.kind === "download") {
        expect(templates, href).toHaveProperty(`../../public${target.href}`);
        continue;
      }
      if (target.kind === "external" && /^https?:/.test(href)) continue;
      // A file of docs/, relative to docs/help, and the heading it points at.
      const path = file
        ? new URL(file, "https://x/docs/help/").pathname
        : `/docs/help/${page.slug}.md`;
      const md = docs[`../../..${path}`];
      expect(md, href).toBeTypeOf("string");
      if (anchor) {
        const ids = parseMarkdown(md as string).flatMap((b) =>
          b.kind === "heading" ? [headingId(b.text)] : [],
        );
        expect(ids, href).toContain(anchor);
      }
    }
  });

  it("lists every column the console maps, and the templates have them all", () => {
    const csv = helpPages.find((p) => p.slug === "csv-files")!.body;
    for (const [field] of [...hrFields, ...accountFields]) expect(csv).toContain(`\`${field}\``);
    const header = (text: string) => text.split("\n")[0]?.split(",");
    // The HR template names people by first and last name, not display_name.
    expect(header(hrTemplate)).toEqual(
      hrFields.map(([f]) => f).filter((f) => f !== "display_name"),
    );
    expect(header(accountsTemplate)).toEqual(accountFields.map(([f]) => f));
  });
});

describe("CSV help next to uploads", () => {
  it("explains an HR export and offers its template", async () => {
    serve({
      "GET /api/v1/me": ok(admin),
      "GET /api/v1/identity-sources": ok({
        sources: [
          {
            id: "hr",
            name: "Workday export",
            kind: "csv",
            config: { missing: "leave", max_leaver_percent: 10 },
            created_at: "2026-10-01T09:00:00Z",
          },
        ],
      }),
    });
    renderAt("/settings/hr-sources");

    const tip = await screen.findByRole("button", { name: "HR export format" });
    expect(tip).toHaveAttribute("aria-expanded", "false");
    await userEvent.click(tip);
    const dialog = screen.getByRole("dialog", { name: "HR export format" });
    const id = within(dialog).getByText("employee_id").closest("tr")!;
    expect(id).toHaveTextContent("Employee id · required");
    expect(within(dialog).getByText(/YYYY-MM-DD, such as 2026-10-31/)).toBeInTheDocument();
    await userEvent.click(within(dialog).getByRole("button", { name: "Close" }));
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    await userEvent.click(tip);
    const reopened = screen.getByRole("dialog", { name: "HR export format" });
    expect(within(reopened).getByRole("link", { name: "Download a template" })).toHaveAttribute(
      "href",
      "/templates/hr-people.csv",
    );
    expect(within(reopened).getByRole("link", { name: "Every rule and error" })).toHaveAttribute(
      "href",
      "/help/csv-files",
    );

    await userEvent.keyboard("{Escape}");
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    expect(tip).toHaveFocus();
    await userEvent.click(tip);
    await userEvent.click(screen.getByText(/HR is the authority/));
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  });

  it("explains an account file with the application's separator", async () => {
    const app: CatalogApplication = {
      id: "erp",
      name: "Legacy ERP",
      connector: "csv",
      config: { separator: "|" },
      provisioning: { mode: "off" },
      read_every_hours: 1,
      entitlement_count: 0,
      created_at: "2026-10-01T09:00:00Z",
    };
    serve({
      "GET /api/v1/me": ok(admin),
      "GET /api/v1/applications": ok({ applications: [app] }),
      "GET /api/v1/applications/erp/entitlements": ok({ entitlements: [] }),
    });
    renderAt("/governance/applications/erp");

    await userEvent.click(await screen.findByRole("button", { name: "Account file format" }));
    const dialog = screen.getByRole("dialog", { name: "Account file format" });
    expect(within(dialog).getByText("account_id").closest("tr")!).toHaveTextContent(
      "Account id · required, or the user name",
    );
    expect(within(dialog).getByText(/separated by \|/)).toBeInTheDocument();
    expect(within(dialog).getByRole("link", { name: "Download a template" })).toHaveAttribute(
      "href",
      "/templates/app-accounts.csv",
    );
  });
});
