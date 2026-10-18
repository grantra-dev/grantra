import { useQueryClient } from "@tanstack/react-query";
import { ChevronDown, Inbox as InboxIcon, Menu, Search, X } from "lucide-react";
import { useEffect, useState } from "react";
import { Link, NavLink, Outlet, useLocation } from "react-router";
import { Button } from "@/components/ui/button";
import { buttonVariants } from "@/components/ui/button-variants";
import { Logo } from "@/components/Logo";
import { SearchDialog } from "@/components/SearchDialog";
import { UserMenu } from "@/components/UserMenu";
import type { Me } from "@/lib/api";
import type { Inbox } from "@/lib/console";
import { useInbox } from "@/lib/hooks";
import { cn } from "@/lib/utils";
import { groupTitle, visibleGroups, visibleItems, type GroupId, type NavItem } from "@/modules";

/** A small count next to a menu item or icon; nothing when zero. */
function Count({ n, label }: { n: number; label: string }) {
  if (n <= 0) return null;
  return (
    <span
      className="ml-auto rounded-full bg-brand-600 px-1.5 py-0.5 text-[11px] leading-none font-semibold text-white"
      aria-label={label}
    >
      {n > 99 ? "99+" : n}
    </span>
  );
}

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

function NavEntry({
  item,
  inbox,
  onNavigate,
}: {
  item: NavItem;
  inbox?: Inbox;
  onNavigate: () => void;
}) {
  const n = item.count && inbox ? item.count(inbox) : 0;
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
      <Count n={n} label={`${n} waiting`} />
    </NavLink>
  );
}

function Navigation({ me, onNavigate }: { me: Me; onNavigate: () => void }) {
  const { pathname } = useLocation();
  const { data: inbox } = useInbox();
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
        .filter((i) => i.top)
        .map((i) => (
          <NavEntry key={i.path} item={i} inbox={inbox} onNavigate={onNavigate} />
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
                  <NavEntry key={i.path} item={i} inbox={inbox} onNavigate={onNavigate} />
                ))}
              </div>
            )}
          </div>
        );
      })}
      <div className="mt-3 border-t border-slate-200 pt-3">
        {items
          .filter((i) => !i.top && !i.group)
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
  const [searching, setSearching] = useState(false);
  const client = useQueryClient();
  const { data: inbox } = useInbox();
  const close = () => setMenuOpen(false);
  // The group name over a section's own page, so people know where they are.
  const group = groupTitle(visibleItems(me).find((i) => i.path === pathname)?.group);

  // Moving to another page refreshes the counters if they are old.
  useEffect(() => {
    void client.refetchQueries({ queryKey: ["inbox"], stale: true, type: "active" });
  }, [pathname, client]);

  // ⌘K or Ctrl+K anywhere, and / outside a text field, open the search.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      const typing =
        e.target instanceof HTMLElement &&
        (e.target.isContentEditable || ["INPUT", "TEXTAREA", "SELECT"].includes(e.target.tagName));
      if (((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === "k") || (e.key === "/" && !typing)) {
        e.preventDefault();
        setSearching(true);
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, []);

  const total = inbox?.total ?? 0;
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
      {searching && <SearchDialog me={me} onClose={() => setSearching(false)} />}
      <div className="flex min-w-0 flex-1 flex-col">
        <header className="flex h-14 items-center gap-2 border-b border-slate-200 bg-white px-3 sm:gap-3 sm:px-5">
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
          <button
            type="button"
            onClick={() => setSearching(true)}
            className="ml-auto flex h-9 items-center gap-2 rounded-md border whitespace-nowrap border-slate-300 px-2.5 text-sm text-slate-500 hover:border-slate-400 hover:text-slate-700 md:mr-auto md:ml-0 md:w-72"
            aria-label="Search Grantra"
            aria-keyshortcuts="Control+K Meta+K"
          >
            <Search className="size-4" aria-hidden />
            <span className="hidden truncate md:inline">Search people, access, pages…</span>
            <kbd className="ml-auto hidden rounded border border-slate-200 px-1.5 text-xs md:inline">
              ⌘K
            </kbd>
          </button>
          <Link
            to="/inbox"
            className={cn(buttonVariants({ variant: "ghost", size: "sm" }), "relative")}
            aria-label={total > 0 ? `Inbox, ${total} waiting` : "Inbox"}
            title="Inbox"
          >
            <InboxIcon />
            {total > 0 && (
              <span className="absolute -top-0.5 -right-0.5 rounded-full bg-brand-600 px-1 text-[10px] leading-4 font-semibold text-white">
                {total > 99 ? "99+" : total}
              </span>
            )}
          </Link>
          <UserMenu me={me} />
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
