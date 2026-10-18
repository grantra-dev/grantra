// Reads the Markdown of the help pages in docs/help. It knows the part of
// Markdown those pages use (headings, paragraphs, lists, tables,
// code, bold, italics and links); components/Markdown renders it as React
// elements, so no HTML from the files ever reaches the page.

export type Block =
  | { kind: "heading"; level: number; text: string }
  | { kind: "paragraph"; text: string }
  | { kind: "list"; ordered: boolean; items: string[] }
  | { kind: "table"; head: string[]; rows: string[][] }
  | { kind: "code"; text: string };

const listItem = /^\s*(?:[-*]|(\d+)\.)\s+(.*)$/;
// Lines that start a block other than a paragraph.
const blockStart = /^(?:#{1,4}\s|```|\||\s*(?:[-*]|\d+\.)\s)/;

export function parseMarkdown(src: string): Block[] {
  const lines = src.replace(/\r\n?/g, "\n").split("\n");
  const blocks: Block[] = [];
  let i = 0;
  // The line at the cursor, undefined past the end.
  const at = () => lines[i];
  for (let line = at(); line !== undefined; line = at()) {
    if (line.trim() === "") {
      i++;
      continue;
    }
    if (line.startsWith("```")) {
      const body: string[] = [];
      for (i++; at() !== undefined && !at()?.startsWith("```"); i++) body.push(at() ?? "");
      i++; // the closing fence
      blocks.push({ kind: "code", text: body.join("\n") });
      continue;
    }
    const heading = /^(#{1,4})\s+(.*)$/.exec(line);
    if (heading) {
      blocks.push({
        kind: "heading",
        level: heading[1]?.length ?? 1,
        text: (heading[2] ?? "").trim(),
      });
      i++;
      continue;
    }
    if (line.startsWith("|")) {
      const rows: string[][] = [];
      for (let row: string | undefined = line; row?.startsWith("|"); row = at()) {
        rows.push(tableCells(row));
        i++;
      }
      // The second row only separates the header from the body.
      const [head = [], , ...body] = rows;
      blocks.push({ kind: "table", head, rows: body });
      continue;
    }
    let item = listItem.exec(line);
    if (item) {
      const ordered = item[1] !== undefined;
      const items: string[] = [];
      while (item) {
        let text = (item[2] ?? "").trim();
        i++;
        // Indented lines carry on the item above.
        for (
          let more = at();
          more !== undefined && /^\s{2,}\S/.test(more) && !listItem.test(more);
          more = at()
        ) {
          text += " " + more.trim();
          i++;
        }
        items.push(text);
        const following = at();
        item = following === undefined ? null : listItem.exec(following);
      }
      blocks.push({ kind: "list", ordered, items });
      continue;
    }
    const text: string[] = [];
    for (
      let more: string | undefined = line;
      more !== undefined && more.trim() !== "" && !blockStart.test(more);
      more = at()
    ) {
      text.push(more.trim());
      i++;
    }
    blocks.push({ kind: "paragraph", text: text.join(" ") });
  }
  return blocks;
}

/** The cells of a table row. A | inside `code` or written \| is text. */
function tableCells(row: string): string[] {
  const cells: string[] = [];
  let cell = "";
  let code = false;
  const s = row.trim().replace(/^\|/, "");
  for (let i = 0; i < s.length; i++) {
    const c = s[i];
    if (c === "\\" && s[i + 1] === "|") {
      cell += "|";
      i++;
    } else if (c === "`") {
      code = !code;
      cell += c;
    } else if (c === "|" && !code) {
      cells.push(cell.trim());
      cell = "";
    } else {
      cell += c;
    }
  }
  if (cell.trim() !== "") cells.push(cell.trim());
  return cells;
}

/** The id GitHub gives a heading, so the same #links work here and there. */
export function headingId(text: string) {
  return text
    .replace(/[`*]/g, "")
    .toLowerCase()
    .replace(/[^\p{L}\p{Nd}\s_-]/gu, "")
    .trim()
    .replace(/\s/g, "-");
}

/** Where the files in docs/help live on GitHub; relative links resolve against it. */
export const helpSource = "https://github.com/grantra-dev/grantra/blob/main/docs/help/";

export type Target =
  | { kind: "anchor" | "external"; href: string }
  | { kind: "page"; to: string }
  | { kind: "download"; href: string }
  | { kind: "none" };

/**
 * Where a link in a help page goes in the console: another help page opens
 * here, a file the console serves (under web/public) downloads, and any
 * other file of the repository opens on GitHub.
 */
export function resolveLink(href: string, pages: ReadonlySet<string>): Target {
  if (href.startsWith("#")) return { kind: "anchor", href };
  if (/^(?:https?|mailto):/i.test(href)) return { kind: "external", href };
  // Any other scheme (javascript:, data:) is not followed.
  if (/^[a-z][a-z\d+.-]*:/i.test(href)) return { kind: "none" };
  const url = new URL(href, helpSource);
  const served = /^\/grantra-dev\/grantra\/blob\/main\/web\/public(\/.+)$/.exec(url.pathname);
  if (served?.[1]) return { kind: "download", href: served[1] };
  const page = /^\/grantra-dev\/grantra\/blob\/main\/docs\/help\/([a-z\d-]+)\.md$/.exec(
    url.pathname,
  );
  if (page?.[1] && pages.has(page[1])) return { kind: "page", to: `/help/${page[1]}${url.hash}` };
  return { kind: "external", href: url.href };
}
