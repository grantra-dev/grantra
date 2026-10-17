import { useId, useState } from "react";
import { Input, Label } from "@/components/ui/field";
import { normalizeColumn, readHeader } from "@/lib/csv";

/** A field Grantra reads: its key, how people call it, and a hint. */
export type Field = readonly [key: string, label: string, hint: string];

/**
 * Which column of a file holds each field, for files that name them their
 * own way. A sample file offers its column names and shows which fields it
 * has.
 */
export function ColumnMapping({
  fields,
  value,
  onChange,
}: {
  fields: readonly Field[];
  value: Record<string, string>;
  onChange: (v: Record<string, string>) => void;
}) {
  const id = useId();
  const [headers, setHeaders] = useState<string[] | null>(null);
  const found = new Set(headers?.map(normalizeColumn));
  return (
    <fieldset className="flex flex-col gap-3">
      <legend className="text-sm font-medium">Column names</legend>
      <p className="text-sm text-slate-500">
        Fill in only the columns your file names differently; the rest are matched by name, ignoring
        case and spaces. Pick a sample file to choose from its columns.
      </p>
      <label className="text-sm">
        <span className="sr-only">Sample file</span>
        <input
          type="file"
          accept=".csv,text/csv"
          aria-label="Sample file"
          onChange={async (e) => {
            const file = e.target.files?.[0];
            setHeaders(file ? await readHeader(file) : null);
          }}
          className="text-sm file:mr-3 file:rounded-md file:border file:border-slate-300 file:bg-white file:px-3 file:py-1.5 file:text-sm"
        />
      </label>
      <datalist id={`${id}-headers`}>
        {headers?.map((h) => (
          <option key={h} value={h} />
        ))}
      </datalist>
      <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
        {fields.map(([key, label, hint]) => {
          const column = value[key]?.trim() || key;
          return (
            <div key={key}>
              <Label htmlFor={`${id}-${key}`}>{label}</Label>
              <Input
                id={`${id}-${key}`}
                list={`${id}-headers`}
                placeholder={key}
                value={value[key] ?? ""}
                onChange={(e) => onChange({ ...value, [key]: e.target.value })}
              />
              <p className="mt-1 text-xs text-slate-500">
                {headers &&
                  (found.has(normalizeColumn(column)) ? (
                    <span className="text-emerald-700">In the file. </span>
                  ) : (
                    <span>Not in the file. </span>
                  ))}
                {hint}
              </p>
            </div>
          );
        })}
      </div>
    </fieldset>
  );
}
