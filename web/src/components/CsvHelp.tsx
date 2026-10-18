import { Download } from "lucide-react";
import { Link } from "react-router";
import { HelpTip } from "@/components/HelpTip";
import { accountFields, hrFields } from "@/lib/admin";

// What each kind of file looks like. The full rules are docs/help/csv-files.md,
// and the templates are in web/public/templates.
const kinds = {
  hr: {
    label: "HR export format",
    rows: "One row per person, with a header row.",
    fields: hrFields,
    required: ["employee_id", "display_name"],
    template: "/templates/hr-people.csv",
    notes: ["Dates are YYYY-MM-DD, such as 2026-10-31.", "An empty status means active."],
  },
  accounts: {
    label: "Account file format",
    rows: "One row per account, or one per account and role, with a header row.",
    fields: accountFields,
    required: ["account_id"],
    template: "/templates/app-accounts.csv",
    notes: ["An empty status means active."],
  },
};

/** The "?" next to a CSV upload: its columns, the main rules and a template. */
export function CsvHelp({
  kind,
  separator,
  align,
}: {
  kind: keyof typeof kinds;
  separator?: string;
  align?: "start" | "end";
}) {
  const k = kinds[kind];
  const notes =
    kind === "accounts"
      ? [...k.notes, `Several entitlements in one cell are separated by ${separator || ";"}`]
      : k.notes;
  return (
    <HelpTip label={k.label} align={align}>
      <h4 className="pr-8 font-semibold text-slate-900">{k.label}</h4>
      <p className="mt-1">
        {k.rows} Separated by commas and saved as UTF-8. Columns with other names work once you map
        them to these fields.
      </p>
      <table className="mt-3 w-full text-xs">
        <thead className="text-slate-500">
          <tr>
            <th className="py-1 pr-3 text-left font-medium">Column</th>
            <th className="py-1 text-left font-medium">Notes</th>
          </tr>
        </thead>
        <tbody className="divide-y divide-slate-100">
          {k.fields.map(([field, label, hint]) => {
            const required = k.required.includes(field);
            return (
              <tr key={field}>
                <td className="py-1 pr-3 font-mono">{field}</td>
                <td className="py-1">
                  {label}
                  {(required || hint) && (
                    <span className="text-slate-500">
                      {" · "}
                      {required && <span className="font-medium text-slate-900">required</span>}
                      {required && hint && hint !== "required" && ", "}
                      {hint !== "required" && hint}
                    </span>
                  )}
                </td>
              </tr>
            );
          })}
        </tbody>
      </table>
      <ul className="mt-3 list-disc pl-5">
        {notes.map((n) => (
          <li key={n}>{n}</li>
        ))}
        <li>Other columns are kept as attributes.</li>
        <li>Check the file first: nothing changes until you confirm.</li>
      </ul>
      <div className="mt-3 flex flex-wrap items-center gap-x-4 gap-y-2">
        <a
          href={k.template}
          download
          className="inline-flex items-center gap-1 font-medium text-brand-600 hover:text-brand-700"
        >
          <Download className="size-4" aria-hidden />
          Download a template
        </a>
        <Link to="/help/csv-files" className="text-brand-600 underline hover:text-brand-700">
          Every rule and error
        </Link>
      </div>
    </HelpTip>
  );
}
