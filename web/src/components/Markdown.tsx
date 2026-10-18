import type { ReactNode } from "react";
import { Link } from "react-router";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { headingId, resolveLink, type Block } from "@/lib/markdown";

const inline = /`([^`]+)`|\*\*(.+?)\*\*|\[([^\]]+)\]\(([^)\s]+)\)|\*([^*\s][^*]*)\*/g;
const linkClass = "text-brand-600 underline hover:text-brand-700";

function Inline({ text, pages }: { text: string; pages: ReadonlySet<string> }) {
  const out: ReactNode[] = [];
  let last = 0;
  for (const m of text.matchAll(inline)) {
    if (m.index > last) out.push(text.slice(last, m.index));
    const key = m.index;
    const [, code, bold, label, href, italic] = m;
    if (code !== undefined) {
      out.push(
        <code key={key} className="rounded bg-slate-100 px-1 py-0.5 font-mono text-[0.85em]">
          {code}
        </code>,
      );
    } else if (bold !== undefined) {
      out.push(
        <strong key={key} className="font-semibold">
          <Inline text={bold} pages={pages} />
        </strong>,
      );
    } else if (italic !== undefined) {
      out.push(
        <em key={key}>
          <Inline text={italic} pages={pages} />
        </em>,
      );
    } else {
      const content = <Inline text={label ?? ""} pages={pages} />;
      const target = resolveLink(href ?? "", pages);
      out.push(
        target.kind === "none" ? (
          <span key={key}>{content}</span>
        ) : target.kind === "page" ? (
          <Link key={key} to={target.to} className={linkClass}>
            {content}
          </Link>
        ) : target.kind === "download" ? (
          <a key={key} href={target.href} download className={linkClass}>
            {content}
          </a>
        ) : target.kind === "external" ? (
          <a key={key} href={target.href} target="_blank" rel="noreferrer" className={linkClass}>
            {content}
          </a>
        ) : (
          <a key={key} href={target.href} className={linkClass}>
            {content}
          </a>
        ),
      );
    }
    last = m.index + m[0].length;
  }
  if (last < text.length) out.push(text.slice(last));
  return <>{out}</>;
}

/** Renders Markdown. pages are the help pages a link may open in the console. */
export function Markdown({ blocks, pages }: { blocks: Block[]; pages: ReadonlySet<string> }) {
  return (
    <div className="flex flex-col gap-4 text-sm leading-6 text-slate-700">
      {blocks.map((b, n) => {
        switch (b.kind) {
          case "heading": {
            const Tag = b.level <= 2 ? "h2" : "h3";
            return (
              <Tag
                key={n}
                id={headingId(b.text)}
                className={
                  Tag === "h2"
                    ? "mt-4 scroll-mt-4 text-lg font-semibold text-slate-900"
                    : "mt-2 scroll-mt-4 font-semibold text-slate-900"
                }
              >
                <Inline text={b.text} pages={pages} />
              </Tag>
            );
          }
          case "paragraph":
            return (
              <p key={n}>
                <Inline text={b.text} pages={pages} />
              </p>
            );
          case "list": {
            const Tag = b.ordered ? "ol" : "ul";
            return (
              <Tag
                key={n}
                className={`flex flex-col gap-1 pl-5 ${b.ordered ? "list-decimal" : "list-disc"}`}
              >
                {b.items.map((item, i) => (
                  <li key={i}>
                    <Inline text={item} pages={pages} />
                  </li>
                ))}
              </Tag>
            );
          }
          case "table":
            return (
              <div key={n} className="rounded-md border border-slate-200">
                <Table>
                  <TableHeader>
                    <TableRow>
                      {b.head.map((h, i) => (
                        <TableHead key={i}>
                          <Inline text={h} pages={pages} />
                        </TableHead>
                      ))}
                    </TableRow>
                  </TableHeader>
                  <TableBody>
                    {b.rows.map((row, r) => (
                      <TableRow key={r}>
                        {b.head.map((_, i) => (
                          <TableCell key={i}>
                            <Inline text={row[i] ?? ""} pages={pages} />
                          </TableCell>
                        ))}
                      </TableRow>
                    ))}
                  </TableBody>
                </Table>
              </div>
            );
          case "code":
            return (
              <pre
                key={n}
                className="overflow-x-auto rounded-md bg-slate-900 p-3 font-mono text-xs text-slate-100"
              >
                {b.text}
              </pre>
            );
        }
      })}
    </div>
  );
}
