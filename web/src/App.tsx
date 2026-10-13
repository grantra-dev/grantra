import { useQuery } from "@tanstack/react-query";
import { Route, Routes } from "react-router";
import { Layout } from "@/components/Layout";
import { ApiError, getMe } from "@/lib/api";
import { visibleSections } from "@/lib/sections";
import { ComingSoon } from "@/pages/ComingSoon";
import { Home } from "@/pages/Home";
import { MyAccess } from "@/pages/MyAccess";
import { NotFound } from "@/pages/NotFound";
import { Centered, LoginNotConfigured, SignIn } from "@/pages/SignIn";

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
  };
  return (
    <Routes>
      <Route element={<Layout me={me} />}>
        {visibleSections(me).map((s) => (
          <Route key={s.path} path={s.path} element={pages[s.path] ?? <ComingSoon section={s} />} />
        ))}
        <Route path="*" element={<NotFound />} />
      </Route>
    </Routes>
  );
}
