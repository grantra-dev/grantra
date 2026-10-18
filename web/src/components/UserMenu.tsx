import { Bell, ChevronDown, LogOut } from "lucide-react";
import { useEffect, useRef, useState } from "react";
import { Link } from "react-router";
import type { Me } from "@/lib/api";

/** The signed-in person's menu: their email settings and signing out. */
export function UserMenu({ me }: { me: Me }) {
  const [open, setOpen] = useState(false);
  const box = useRef<HTMLDivElement>(null);

  useEffect(() => {
    if (!open) return;
    const away = (e: MouseEvent) => {
      if (!box.current?.contains(e.target as Node)) setOpen(false);
    };
    document.addEventListener("mousedown", away);
    return () => document.removeEventListener("mousedown", away);
  }, [open]);

  const initial = me.display_name.trim()[0]?.toUpperCase() ?? "?";
  return (
    <div ref={box} className="relative" onKeyDown={(e) => e.key === "Escape" && setOpen(false)}>
      <button
        type="button"
        onClick={() => setOpen((o) => !o)}
        aria-haspopup="menu"
        aria-expanded={open}
        aria-label={`Account of ${me.display_name}`}
        className="flex items-center gap-2 rounded-md px-1.5 py-1 text-left text-sm hover:bg-slate-100"
      >
        <span
          className="flex size-7 items-center justify-center rounded-full bg-brand-50 font-medium text-brand-700"
          aria-hidden
        >
          {initial}
        </span>
        <span className="hidden leading-tight sm:block">
          <span className="block font-medium">{me.display_name}</span>
          {me.email && <span className="block text-xs text-slate-500">{me.email}</span>}
        </span>
        <ChevronDown className="size-3.5 text-slate-400" aria-hidden />
      </button>
      {open && (
        <div
          role="menu"
          className="absolute right-0 z-30 mt-1 w-60 rounded-md border border-slate-200 bg-white p-1 text-sm shadow-lg"
        >
          <div className="border-b border-slate-100 px-3 py-2 sm:hidden">
            <div className="font-medium">{me.display_name}</div>
            {me.email && <div className="text-xs text-slate-500">{me.email}</div>}
          </div>
          <Link
            to="/settings/notifications"
            role="menuitem"
            onClick={() => setOpen(false)}
            className="flex items-center gap-2 rounded px-3 py-2 hover:bg-slate-100"
          >
            <Bell className="size-4" aria-hidden />
            Email notifications
          </Link>
          {/* A plain form post: the server ends the session and redirects to
              the login provider to end that session too. */}
          <form method="post" action="/auth/logout">
            <button
              type="submit"
              role="menuitem"
              className="flex w-full items-center gap-2 rounded px-3 py-2 text-left hover:bg-slate-100"
            >
              <LogOut className="size-4" aria-hidden />
              Sign out
            </button>
          </form>
        </div>
      )}
    </div>
  );
}
