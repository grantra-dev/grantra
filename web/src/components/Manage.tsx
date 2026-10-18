import { Eye } from "lucide-react";
import type { ReactNode } from "react";
import { useCanManage } from "@/lib/manage";

/** Shows its children only to people who may change the page. */
export function Manage({ children }: { children: ReactNode }) {
  return useCanManage() ? children : null;
}

/** Says, on a read-only page, why there is nothing to click. */
export function ReadOnlyNotice() {
  if (useCanManage()) return null;
  return (
    <p className="mb-4 flex items-center gap-2 rounded-md border border-slate-200 bg-slate-50 px-3 py-2 text-sm text-slate-600">
      <Eye className="size-4 shrink-0" aria-hidden />
      You can look at everything here, but only admins change it.
    </p>
  );
}

/** Shows settings to everyone, but lets only people who may change the page edit them. */
export function ReadOnlyFields({ children }: { children: ReactNode }) {
  return (
    <fieldset disabled={!useCanManage()} className="contents">
      {children}
    </fieldset>
  );
}
