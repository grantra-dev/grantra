import { Link } from "react-router";
import { Badge } from "@/components/ui/badge";
import { edgeLabels, stepLabels, type AttackPath, type Step } from "@/lib/insights";
import { formatDate } from "@/lib/utils";

/** Where a step of a path links to: people have a page. */
const stepLink = (s: Step) => (s.kind === "person" && s.id ? `/people/${s.id}` : undefined);

/** One way to some access, read left to right: who, how, through what. */
export function PathChain({ path: p }: { path: AttackPath }) {
  return (
    <div className="flex flex-col gap-1">
      <ol className="flex flex-wrap items-center gap-x-1.5 gap-y-1 text-sm" aria-label="Path">
        {p.steps.map((s, i) => {
          const to = stepLink(s);
          const name = to ? (
            <Link to={to} className="font-medium text-brand-700 hover:underline">
              {s.name}
            </Link>
          ) : (
            <span className="font-medium">{s.name}</span>
          );
          return (
            <li key={i} className="flex flex-wrap items-center gap-1.5">
              {/* Spaces as well as gaps, so the path reads as a sentence when copied or read aloud. */}
              {s.edge && <span className="text-slate-500">{edgeLabels[s.edge]} </span>}
              <span
                className="rounded-md border border-slate-200 bg-white px-1.5 py-0.5"
                title={stepLabels[s.kind]}
              >
                {s.kind !== "person" && (
                  <span className="text-xs text-slate-500">{stepLabels[s.kind]} </span>
                )}
                {name}
                {s.detail && <span className="text-slate-500"> · {s.detail}</span>}
              </span>{" "}
            </li>
          );
        })}
      </ol>
      <div className="flex flex-wrap gap-1.5">
        {p.standing ? (
          <Badge variant="red">For good</Badge>
        ) : p.until ? (
          <Badge>Until {formatDate(p.until)}</Badge>
        ) : (
          <Badge variant="green">Only when turned on</Badge>
        )}
        {p.credential && <Badge variant="amber">Through a shared account</Badge>}
        {p.outside && <Badge variant="amber">Not given by Grantra</Badge>}
      </div>
    </div>
  );
}
