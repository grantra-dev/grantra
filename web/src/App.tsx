import { useQuery } from "@tanstack/react-query";
import { lazy, Suspense, type ReactNode } from "react";
import { Navigate, Route, Routes, useLocation, useParams, useSearchParams } from "react-router";
import { Layout } from "@/components/Layout";
import { ReadOnlyNotice } from "@/components/Manage";
import { PageHeader } from "@/components/PageHeader";
import { ApiError, getMe, type Me } from "@/lib/api";
import { ManageContext } from "@/lib/manage";
import { isAdmin } from "@/lib/roles";
import { visibleItems, type NavItem } from "@/modules";
import { ComingSoon } from "@/pages/ComingSoon";
import { NotFound } from "@/pages/NotFound";
import { NotificationSettings } from "@/pages/NotificationSettings";
import { Centered, LoginNotConfigured, SignIn } from "@/pages/SignIn";

const Desktop = lazy(() => import("@/pages/Desktop").then((m) => ({ default: m.Desktop })));

const loading = <p className="text-sm text-slate-500">Loading…</p>;

/** Sends Databases & clusters' old addresses to Remote access. */
function OldRemoteAccess() {
  const { pathname, search } = useLocation();
  return <Navigate to={pathname.replace(/^\/databases/, "/remote-access") + search} replace />;
}

// Where each tab of the old Administration page lives now. Links to the old
// addresses are in emails already sent, so they keep working.
const oldAdminTabs: Record<string, string> = {
  sources: "/settings/hr-sources",
  applications: "/governance/applications",
  access: "/governance/roles",
  sod: "/governance/separation-of-duties",
  standing: "/governance/standing-access",
  login: "/settings/grantra-login",
  tokens: "/settings/api-tokens",
};

function OldAdministration() {
  const [params] = useSearchParams();
  return <Navigate to={oldAdminTabs[params.get("tab") ?? ""] ?? "/settings/hr-sources"} replace />;
}

function OldApplication() {
  const { id } = useParams();
  const { search } = useLocation();
  return <Navigate to={`/governance/applications/${id}${search}`} replace />;
}

function itemPage(item: NavItem, me: Me): ReactNode {
  if (!item.page) return <ComingSoon section={item} />;
  return (
    <Managed item={item} me={me}>
      <Suspense fallback={loading}>
        {item.header && <PageHeader title={item.title} description={item.description} />}
        <ReadOnlyNotice />
        <item.page me={me} />
      </Suspense>
    </Managed>
  );
}

/** Settings pages, and the pages under them, are read-only for anyone but admins. */
function Managed({ item, me, children }: { item: NavItem; me: Me; children: ReactNode }) {
  return (
    <ManageContext.Provider value={!item.manages || isAdmin(me)}>{children}</ManageContext.Provider>
  );
}

export function App() {
  const { data: me, error, isPending } = useQuery({ queryKey: ["me"], queryFn: getMe });

  if (isPending) {
    return <p className="p-8 text-sm text-slate-500">Loading…</p>;
  }
  if (error instanceof ApiError && error.status === 401) return <SignIn />;
  if (error instanceof ApiError && error.status === 503) return <LoginNotConfigured />;
  if (error) {
    return (
      <Centered>
        <p role="alert" className="p-5 text-sm text-red-700">
          Grantra could not load your profile: {error.message}
        </p>
      </Centered>
    );
  }

  // Only what this person sees gets a route; anything else is "not found".
  const items = visibleItems(me);
  return (
    <Routes>
      {/* A desktop takes its whole tab. */}
      <Route
        path="/remote-access/desktops/:id"
        element={
          <Suspense fallback={<p className="p-8 text-sm text-slate-500">Loading…</p>}>
            <Desktop />
          </Suspense>
        }
      />
      <Route element={<Layout me={me} />}>
        {items.map((i) => (
          <Route key={i.path} path={i.path} element={itemPage(i, me)} />
        ))}
        {items.flatMap((i) =>
          (i.subpages ?? []).map((s) => (
            <Route
              key={s.path}
              path={s.path}
              element={
                <Managed item={i} me={me}>
                  <Suspense fallback={loading}>
                    <ReadOnlyNotice />
                    <s.page me={me} />
                  </Suspense>
                </Managed>
              }
            />
          )),
        )}
        {/* Old addresses, from before the menu had groups. */}
        <Route path="/admin" element={<OldAdministration />} />
        <Route path="/admin/applications/:id" element={<OldApplication />} />
        <Route path="/databases/*" element={<OldRemoteAccess />} />
        <Route path="/settings/notifications" element={<NotificationSettings me={me} />} />
        <Route path="*" element={<NotFound />} />
      </Route>
    </Routes>
  );
}
