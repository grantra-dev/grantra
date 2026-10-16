import { useQuery } from "@tanstack/react-query";
import { Route, Routes } from "react-router";
import { Layout } from "@/components/Layout";
import { ApiError, getMe } from "@/lib/api";
import { visibleSections } from "@/lib/sections";
import { AccessReviews } from "@/pages/AccessReviews";
import { Administration } from "@/pages/admin/Administration";
import { Application } from "@/pages/admin/Application";
import { Approvals } from "@/pages/Approvals";
import { AuditLog } from "@/pages/AuditLog";
import { ComingSoon } from "@/pages/ComingSoon";
import { Home } from "@/pages/Home";
import { MyAccess } from "@/pages/MyAccess";
import { MyRequests } from "@/pages/MyRequests";
import { NotFound } from "@/pages/NotFound";
import { NotificationSettings } from "@/pages/NotificationSettings";
import { People } from "@/pages/People";
import { Person } from "@/pages/Person";
import { RequestAccess } from "@/pages/RequestAccess";
import { Centered, LoginNotConfigured, SignIn } from "@/pages/SignIn";
import { Vault } from "@/pages/Vault";

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

  const pages: Record<string, React.ReactNode> = {
    "/": <Home me={me} />,
    "/my-access": <MyAccess me={me} />,
    "/request": <RequestAccess me={me} />,
    "/requests": <MyRequests me={me} />,
    "/vault": <Vault me={me} />,
    "/approvals": <Approvals me={me} />,
    "/reviews": <AccessReviews me={me} />,
    "/people": <People />,
    "/audit": <AuditLog />,
    "/admin": <Administration />,
  };
  // Pages under a section, shown to whoever sees the section.
  const subpages: Record<string, [string, React.ReactNode]> = {
    "/people": ["/people/:id", <Person me={me} />],
    "/admin": ["/admin/applications/:id", <Application />],
  };
  return (
    <Routes>
      <Route element={<Layout me={me} />}>
        {visibleSections(me).map((s) => (
          <Route key={s.path} path={s.path} element={pages[s.path] ?? <ComingSoon section={s} />} />
        ))}
        {visibleSections(me).flatMap((s) => {
          const sub = subpages[s.path];
          return sub ? [<Route key={sub[0]} path={sub[0]} element={sub[1]} />] : [];
        })}
        <Route path="/settings/notifications" element={<NotificationSettings me={me} />} />
        <Route path="*" element={<NotFound />} />
      </Route>
    </Routes>
  );
}
