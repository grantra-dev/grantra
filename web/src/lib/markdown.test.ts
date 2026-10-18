import { describe, expect, it } from "vitest";
import { headingId, parseMarkdown, resolveLink } from "./markdown";

describe("parseMarkdown", () => {
  it("reads the blocks the help pages use", () => {
    const blocks = parseMarkdown(
      [
        "## What you can access",
        "",
        "A paragraph that",
        "goes on.",
        "- one",
        "  still one",
        "- two",
        "",
        "1. first",
        "2. second",
        "",
        "| Column | Meaning |",
        "|---|---|",
        "| `a|b` | x \\| y |",
        "| empty | |",
        "```sh",
        "curl -X POST",
        "```",
      ].join("\n"),
    );
    expect(blocks).toEqual([
      { kind: "heading", level: 2, text: "What you can access" },
      { kind: "paragraph", text: "A paragraph that goes on." },
      { kind: "list", ordered: false, items: ["one still one", "two"] },
      { kind: "list", ordered: true, items: ["first", "second"] },
      {
        kind: "table",
        head: ["Column", "Meaning"],
        rows: [
          ["`a|b`", "x | y"],
          ["empty", ""],
        ],
      },
      { kind: "code", text: "curl -X POST" },
    ]);
  });

  it("ends on files without a final newline or closing fence", () => {
    expect(parseMarkdown("text")).toEqual([{ kind: "paragraph", text: "text" }]);
    expect(parseMarkdown("```\ncode")).toEqual([{ kind: "code", text: "code" }]);
    expect(parseMarkdown("")).toEqual([]);
  });
});

describe("headingId", () => {
  it("matches GitHub's anchors", () => {
    expect(headingId("When a file is refused")).toBe("when-a-file-is-refused");
    expect(headingId("The `status` column, again!")).toBe("the-status-column-again");
  });
});

describe("resolveLink", () => {
  const pages = new Set(["csv-files"]);
  it("opens help pages in the console", () => {
    expect(resolveLink("csv-files.md#when-a-file-is-refused", pages)).toEqual({
      kind: "page",
      to: "/help/csv-files#when-a-file-is-refused",
    });
  });
  it("downloads the files the console serves", () => {
    expect(resolveLink("../../web/public/templates/hr-people.csv", pages)).toEqual({
      kind: "download",
      href: "/templates/hr-people.csv",
    });
  });
  it("sends the rest to GitHub", () => {
    expect(resolveLink("../connectors.md#csv-files", pages)).toEqual({
      kind: "external",
      href: "https://github.com/grantra-dev/grantra/blob/main/docs/connectors.md#csv-files",
    });
    // A page this person does not see.
    expect(resolveLink("administration.md", pages).kind).toBe("external");
    expect(resolveLink("https://grantra.dev", pages)).toEqual({
      kind: "external",
      href: "https://grantra.dev",
    });
    expect(resolveLink("#top", pages)).toEqual({ kind: "anchor", href: "#top" });
  });
  it("follows no other scheme", () => {
    expect(resolveLink("javascript:alert(1)", pages)).toEqual({ kind: "none" });
    expect(resolveLink("data:text/html,x", pages)).toEqual({ kind: "none" });
  });
});
