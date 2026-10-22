import { Check } from "lucide-react";
import { cn } from "@/lib/utils";

/** Where a guided flow is: done steps ticked, the current one highlighted. */
export function Steps({ steps, current }: { steps: readonly string[]; current: number }) {
  return (
    <ol className="mt-2 flex flex-wrap gap-x-4 gap-y-2 text-sm" aria-label="Steps">
      {steps.map((s, i) => (
        <li
          key={s}
          aria-current={i === current ? "step" : undefined}
          className={cn(
            "flex items-center gap-1.5",
            i === current ? "font-medium text-brand-700" : "text-slate-500",
          )}
        >
          <span
            className={cn(
              "flex size-5 items-center justify-center rounded-full text-xs",
              i < current ? "bg-brand-600 text-white" : "border border-current",
            )}
          >
            {i < current ? <Check className="size-3" aria-hidden /> : i + 1}
          </span>
          {s}
        </li>
      ))}
    </ol>
  );
}
