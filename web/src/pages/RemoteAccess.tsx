import { useSearchParams } from "react-router";
import { PageHeader } from "@/components/PageHeader";
import { Tabs } from "@/components/Tabs";
import { PlatformRole, type Me } from "@/lib/api";
import { Targets } from "./remote-access/Targets";
import { Queries, Sessions } from "./remote-access/Sessions";

type Tab = "targets" | "sessions" | "queries";

export function RemoteAccess({ me }: { me: Me }) {
  const admin = me.platform_roles.includes(PlatformRole.Admin);
  const reader = admin || me.platform_roles.includes(PlatformRole.Auditor);
  const [params, setParams] = useSearchParams();
  const asked = params.get("tab");
  const tab: Tab = reader && (asked === "sessions" || asked === "queries") ? asked : "targets";
  return (
    <>
      <PageHeader
        title="Remote access"
        description="Databases, Kubernetes clusters, desktops and cloud consoles, with access that lasts only as long as yours. Nobody sees the passwords; queries and requests are logged, desktops recorded."
      />
      {reader && (
        <Tabs
          tabs={[
            ["targets", "Targets"],
            ["sessions", "Sessions"],
            ["queries", "Queries"],
          ]}
          current={tab}
          onSelect={(t) => setParams(t === "targets" ? {} : { tab: t })}
        />
      )}
      {tab === "targets" ? (
        <Targets admin={admin} reader={reader} />
      ) : tab === "sessions" ? (
        <Sessions admin={admin} />
      ) : (
        <Queries />
      )}
    </>
  );
}
