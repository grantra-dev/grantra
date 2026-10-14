import { Bell, LogOut } from "lucide-react";
import { Link, NavLink, Outlet } from "react-router";
import { Button } from "@/components/ui/button";
import { buttonVariants } from "@/components/ui/button-variants";
import { Logo } from "@/components/Logo";
import type { Me } from "@/lib/api";
import { visibleSections } from "@/lib/sections";
import { cn } from "@/lib/utils";

export function Layout({ me }: { me: Me }) {
  return (
    <div className="flex min-h-screen flex-col md:flex-row">
      <aside className="border-b border-slate-200 bg-white md:w-64 md:shrink-0 md:border-r md:border-b-0">
        <div className="flex h-14 items-center px-5">
          <Logo />
        </div>
        <nav aria-label="Main" className="flex gap-1 overflow-x-auto px-3 pb-3 md:flex-col">
          {visibleSections(me).map((s) => (
            <NavLink
              key={s.path}
              to={s.path}
              end={s.path === "/"}
              className={({ isActive }) =>
                cn(
                  "flex items-center gap-3 rounded-md px-3 py-2 text-sm whitespace-nowrap text-slate-600 hover:bg-slate-100 hover:text-slate-900",
                  isActive && "bg-brand-50 font-medium text-brand-700 hover:bg-brand-50",
                )
              }
            >
              <s.icon className="size-4" aria-hidden />
              {s.title}
            </NavLink>
          ))}
        </nav>
      </aside>
      <div className="flex min-w-0 flex-1 flex-col">
        <header className="flex h-14 items-center justify-end gap-3 border-b border-slate-200 bg-white px-5">
          <div className="text-right text-sm leading-tight">
            <div className="font-medium">{me.display_name}</div>
            {me.email && <div className="text-slate-500">{me.email}</div>}
          </div>
          <Link
            to="/settings/notifications"
            className={buttonVariants({ variant: "ghost", size: "sm" })}
            aria-label="Email notifications"
            title="Email notifications"
          >
            <Bell />
          </Link>
          {/* A plain form post: the server ends the session and redirects to
              the login provider to end that session too. */}
          <form method="post" action="/auth/logout">
            <Button type="submit" variant="ghost" size="sm" aria-label="Sign out">
              <LogOut />
              <span className="hidden sm:inline">Sign out</span>
            </Button>
          </form>
        </header>
        <main className="mx-auto w-full max-w-6xl flex-1 p-5 md:p-8">
          <Outlet />
        </main>
      </div>
    </div>
  );
}
