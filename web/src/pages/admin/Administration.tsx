import { useSearchParams } from "react-router";
import { PageHeader } from "@/components/PageHeader";
import { Tabs } from "@/components/Tabs";
import { AccessModel } from "./AccessModel";
import { Applications } from "./Applications";
import { GrantraLogin } from "./GrantraLogin";
import { HrSources } from "./HrSources";
import { SeparationOfDuties } from "./SeparationOfDuties";
import { StandingAccess } from "./StandingAccess";

const tabs = [
  ["sources", "HR sources"],
  ["applications", "Applications"],
  ["access", "Roles and rules"],
  ["sod", "Separation of duties"],
  ["standing", "Standing access"],
  ["login", "Grantra Login"],
] as const;

type Tab = (typeof tabs)[number][0];

export function Administration() {
  const [params, setParams] = useSearchParams();
  const asked = params.get("tab");
  const tab: Tab = tabs.find(([k]) => k === asked)?.[0] ?? "sources";
  return (
    <>
      <PageHeader
        title="Administration"
        description="Where people come from, what they can get, and the rules that keep it safe."
      />
      <Tabs
        tabs={tabs}
        current={tab}
        onSelect={(key) => setParams(key === "sources" ? {} : { tab: key })}
      />
      {tab === "sources" && <HrSources />}
      {tab === "applications" && <Applications />}
      {tab === "access" && <AccessModel />}
      {tab === "sod" && <SeparationOfDuties />}
      {tab === "standing" && <StandingAccess />}
      {tab === "login" && <GrantraLogin />}
    </>
  );
}
