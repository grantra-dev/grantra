import { cn } from "@/lib/utils";

/** A row of tabs; the page decides what each one shows. */
export function Tabs<K extends string>({
  tabs,
  current,
  onSelect,
}: {
  tabs: readonly (readonly [K, string])[];
  current: K;
  onSelect: (key: K) => void;
}) {
  return (
    <div role="tablist" className="mb-4 flex flex-wrap gap-1 border-b border-slate-200">
      {tabs.map(([key, label]) => (
        <button
          key={key}
          role="tab"
          aria-selected={current === key}
          onClick={() => onSelect(key)}
          className={cn(
            "-mb-px border-b-2 px-3 py-2 text-sm",
            current === key
              ? "border-brand-600 font-medium text-brand-700"
              : "border-transparent text-slate-500 hover:text-slate-900",
          )}
        >
          {label}
        </button>
      ))}
    </div>
  );
}
