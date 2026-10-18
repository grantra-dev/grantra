import { Bell, ChevronDown, LogOut, Menu, X } from "lucide-react";
import { useState } from "react";
import { Link, NavLink, Outlet, useLocation } from "react-router";
import { Button } from "@/components/ui/button";
import { buttonVariants } from "@/components/ui/button-variants";
import { Logo } from "@/components/Logo";
import type { Me } from "@/lib/api";
import { cn } from "@/lib/utils";
import { groupTitle, visibleGroups, visibleItems, type GroupId, type NavItem } from "@/modules";

const collapsedKey = "grantra.nav.collapsed";

// Which groups someone folded, kept in this browser only. Storage can be
// missing or blocked (private windows), so the menu works without it.
function loadCollapsed(): GroupId[] {
  try {
    const v: unknown = JSON.parse(localStorage.getItem(collapsedKey) ?? "[]");
    return Array.isArray(v) ? (v as GroupId[]) : [];
  } catch {
    return [];
  }
}

function saveCollapsed(ids: GroupId[]) {
  try {
    localStorage.setItem(collapsedKey, JSON.stringify(ids));
  } catch {
    // Folding still works for this visit.
  }
}

/** Whether the page at pathname belongs to the item: its own page or one under it. */
const within = (item: NavItem, pathname: string) =>
  item.path === "/"
    ? pathname === "/"
    : pathname === item.path || pathname.startsWith(item.path + "/");

function NavEntry({ item, onNavigate }: { item: NavItem; onNavigate: () => void }) {
  return (
    <NavLink
      to={item.path}
      end={item.path === "/"}
      onClick={onNavigate}
      className={({ isActive }) =>
        cn(
          "flex items-center gap-3 rounded-md px-3 py-2 text-sm text-slate-600 hover:bg-slate-100 hover:text-slate-900",
          isActive && "bg-brand-50 font-medium text-brand-700 hover:bg-brand-50",
        )
      }
    >
      <item.icon className="size-4 shrink-0" aria-hidden />
      {item.title}
    </NavLink>
  );
}

function Navigation({ me, onNavigate }: { me: Me; onNavigate: () => void }) {
  const { pathname } = useLocation();
  const [collapsed, setCollapsed] = useState(loadCollapsed);
  const items = visibleItems(me);
  const toggle = (id: GroupId) => {
    const next = collapsed.includes(id) ? collapsed.filter((c) => c !== id) : [...collapsed, id];
    setCollapsed(next);
    saveCollapsed(next);
  };
  return (
    <nav aria-label="Main" className="flex flex-col gap-1 px-3 pb-4">
      {items
        .filter((i) => i.path === "/")
        .map((i) => (
          <NavEntry key={i.path} item={i} onNavigate={onNavigate} />
        ))}
      {visibleGroups(me).map((g) => {
        // The group of the page you are on never hides it.
        const open = !collapsed.includes(g.id) || g.items.some((i) => within(i, pathname));
        return (
          <div key={g.id} className="mt-3">
            <button
              type="button"
              onClick={() => toggle(g.id)}
              aria-expanded={open}
              className="flex w-full items-center justify-between rounded-md px-3 py-1 text-xs font-semibold tracking-wide text-slate-500 uppercase hover:text-slate-900"
            >
              {g.title}
              <ChevronDown
                className={cn("size-3.5 transition-transform", !open && "-rotate-90")}
                aria-hidden
              />
            </button>
            {open && (
              <div className="mt-1 flex flex-col gap-1">
                {g.items.map((i) => (
                  <NavEntry key={i.path} item={i} onNavigate={onNavigate} />
                ))}
              </div>
            )}
          </div>
        );
      })}
      <div className="mt-3 border-t border-slate-200 pt-3">
        {items
          .filter((i) => i.path !== "/" && !i.group)
          .map((i) => (
            <NavEntry key={i.path} item={i} onNavigate={onNavigate} />
          ))}
      </div>
    </nav>
  );
}

export function Layout({ me }: { me: Me }) {
  const { pathname } = useLocation();
  const [menuOpen, setMenuOpen] = useState(false);
  const close = () => setMenuOpen(false);
  // The group name over a section's own page, so people know where they are.
  const group = groupTitle(visibleItems(me).find((i) => i.path === pathname)?.group);
  return (
    <div className="flex min-h-screen">
      <aside className="sticky top-0 hidden h-screen w-64 shrink-0 overflow-y-auto border-r border-slate-200 bg-white md:block">
        <div className="flex h-14 items-center px-5">
          <Logo />
        </div>
        <Navigation me={me} onNavigate={close} />
      </aside>
      {menuOpen && (
        <div
          className="fixed inset-0 z-40 md:hidden"
          role="dialog"
          aria-modal="true"
          aria-label="Menu"
          onKeyDown={(e) => e.key === "Escape" && close()}
        >
          <div className="absolute inset-0 bg-slate-900/30" onClick={close} aria-hidden />
          <aside className="absolute inset-y-0 left-0 w-72 max-w-[85vw] overflow-y-auto bg-white shadow-xl">
            <div className="flex h-14 items-center justify-between px-5">
              <Logo />
              <Button variant="ghost" size="sm" onClick={close} aria-label="Close menu" autoFocus>
                <X />
              </Button>
            </div>
            <Navigation me={me} onNavigate={close} />
          </aside>
        </div>
      )}
      <div className="flex min-w-0 flex-1 flex-col">
        <header className="flex h-14 items-center gap-3 border-b border-slate-200 bg-white px-3 sm:px-5">
          <Button
            variant="ghost"
            size="sm"
            className="md:hidden"
            onClick={() => setMenuOpen(true)}
            aria-label="Open menu"
            aria-expanded={menuOpen}
          >
            <Menu />
          </Button>
          <div className="md:hidden">
            <Logo />
          </div>
          <div className="ml-auto hidden text-right text-sm leading-tight sm:block">
            <div className="font-medium">{me.display_name}</div>
            {me.email && <div className="text-slate-500">{me.email}</div>}
          </div>
          <Link
            to="/settings/notifications"
            className={cn(buttonVariants({ variant: "ghost", size: "sm" }), "ml-auto sm:ml-0")}
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
        <main className="mx-auto w-full max-w-6xl flex-1 p-4 md:p-8">
          {group && (
            <p className="mb-1 text-xs font-semibold tracking-wide text-slate-500 uppercase">
              {group}
            </p>
          )}
          <Outlet />
        </main>
      </div>
    </div>
  );
}
