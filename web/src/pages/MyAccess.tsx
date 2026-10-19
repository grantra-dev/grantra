import { useQuery } from "@tanstack/react-query";
import { useState } from "react";
import { FilterBox } from "@/components/FilterBox";
import { PageHeader } from "@/components/PageHeader";
import { AccessTable } from "@/components/AccessBadges";
import { Eligibilities } from "@/components/Eligibilities";
import { Card } from "@/components/ui/card";
import { getIdentityAccess, type Me } from "@/lib/api";
import { hasWords } from "@/lib/utils";

export function MyAccess({ me }: { me: Me }) {
  const [filter, setFilter] = useState("");
  const { data, error, isPending } = useQuery({
    queryKey: ["identity-access", me.identity_id],
    queryFn: () => getIdentityAccess(me.identity_id),
  });

  const rows = (data ?? []).filter((a) => hasWords(filter, a.name, a.application));

  return (
    <>
      <PageHeader
        title="My access"
        description="Everything you can access through Grantra, why you have it and when it ends."
      />
      <Eligibilities identityId={me.identity_id} self canManage={false} />
      <Card>
        <div className="flex flex-wrap items-center justify-between gap-3 border-b border-slate-200 p-4">
          <FilterBox label="Filter by name or application" value={filter} onChange={setFilter} />
          {data && (
            <span className="text-sm text-slate-500">
              {data.length === 1 ? "1 access" : `${data.length} accesses`}
            </span>
          )}
        </div>
        {isPending ? (
          <p className="p-6 text-sm text-slate-500">Loading your access…</p>
        ) : error ? (
          <p role="alert" className="p-6 text-sm text-red-700">
            Could not load your access: {error.message}
          </p>
        ) : data.length === 0 ? (
          <p className="p-6 text-sm text-slate-500">
            You have no access through Grantra yet. Birthright access appears here once an HR source
            assigns it, and anything you request appears once it is approved.
          </p>
        ) : rows.length === 0 ? (
          <p className="p-6 text-sm text-slate-500">Nothing matches “{filter}”.</p>
        ) : (
          <AccessTable rows={rows} who="you" />
        )}
      </Card>
    </>
  );
}
