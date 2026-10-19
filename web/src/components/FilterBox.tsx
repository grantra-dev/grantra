import { Search } from "lucide-react";
import { cn } from "@/lib/utils";

/** A search box that narrows a list; label is its placeholder unless one is given. */
export function FilterBox({
  label,
  placeholder = label,
  value,
  onChange,
  className,
}: {
  label: string;
  placeholder?: string;
  value: string;
  onChange: (value: string) => void;
  className?: string;
}) {
  return (
    <label className={cn("relative block w-full max-w-xs", className)}>
      <span className="sr-only">{label}</span>
      <Search
        className="pointer-events-none absolute top-2.5 left-3 size-4 text-slate-400"
        aria-hidden
      />
      <input
        type="search"
        value={value}
        onChange={(e) => onChange(e.target.value)}
        placeholder={placeholder}
        className="h-9 w-full rounded-md border border-slate-300 pr-3 pl-9 text-sm focus:outline-2 focus:outline-brand-600"
      />
    </label>
  );
}
