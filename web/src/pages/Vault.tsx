import { useState } from "react";
import { useSearchParams } from "react-router";
import { PageHeader } from "@/components/PageHeader";
import { Tabs } from "@/components/Tabs";
import { PlatformRole, type Me } from "@/lib/api";
import { type NewVaultAccount } from "@/lib/vault";
import { Accounts } from "./vault/Accounts";
import { Discovery } from "./vault/Discovery";
import { BreakGlassUses } from "./vault/BreakGlass";

type Tab = "accounts" | "break-glass" | "discovery";

export function Vault({ me }: { me: Me }) {
  const admin = me.platform_roles.includes(PlatformRole.Admin);
  const reader = admin || me.platform_roles.includes(PlatformRole.Auditor);
  const [params, setParams] = useSearchParams();
  const asked = params.get("tab");
  const tab: Tab =
    reader && (asked === "break-glass" || asked === "discovery") ? asked : "accounts";
  const setTab = (t: Tab) => setParams(t === "accounts" ? {} : { tab: t });
  const [adding, setAdding] = useState<Partial<NewVaultAccount> | null>(null);
  return (
    <>
      <PageHeader
        title="Vault"
        description="Passwords and keys of privileged accounts. Check one out when you need it; Grantra records who, when and why, and can change the password after."
      />
      {reader && (
        <Tabs
          tabs={[
            ["accounts", "Accounts"],
            ["break-glass", "Break glass"],
            ["discovery", "Discovery"],
          ]}
          current={tab}
          onSelect={setTab}
        />
      )}
      {tab === "accounts" ? (
        <Accounts me={me} admin={admin} reader={reader} adding={adding} setAdding={setAdding} />
      ) : tab === "break-glass" ? (
        <BreakGlassUses me={me} admin={admin} />
      ) : (
        <Discovery
          admin={admin}
          onAdd={(f) => {
            setAdding({
              name: `${f.application_name} ${f.username || f.native_id}`,
              account_id: f.account_id,
            });
            setTab("accounts");
          }}
        />
      )}
    </>
  );
}
