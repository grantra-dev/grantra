import { useQuery } from "@tanstack/react-query";
import { Link } from "react-router";
import { Manage } from "@/components/Manage";
import { Badge } from "@/components/ui/badge";
import { buttonVariants } from "@/components/ui/button-variants";
import { Card } from "@/components/ui/card";
import { connectorLabels, listCatalogApplications } from "@/lib/admin";
import { connectorTypes } from "@/lib/connectors";

/** Settings → Connectors: what Grantra can connect to; connecting one opens Add application. */
export function Connectors() {
  const apps = useQuery({ queryKey: ["catalog-applications"], queryFn: listCatalogApplications });
  const connected = (apps.data ?? []).filter(
    (a) => a.connector !== "manual" && a.connector !== "grantra",
  );

  return (
    <div className="flex flex-col gap-6">
      <section aria-labelledby="connected">
        <h2 id="connected" className="mb-2 text-sm font-semibold text-slate-700">
          Connected
        </h2>
        <Card>
          {apps.isPending ? (
            <p className="p-4 text-sm text-slate-500">Loading…</p>
          ) : apps.error ? (
            <p role="alert" className="p-4 text-sm text-red-700">
              Could not load the applications: {apps.error.message}
            </p>
          ) : connected.length === 0 ? (
            <p className="p-4 text-sm text-slate-500">
              No application is connected yet. Pick one below.
            </p>
          ) : (
            <ul className="divide-y divide-slate-100">
              {connected.map((a) => (
                <li key={a.id} className="flex items-center justify-between gap-3 p-4 text-sm">
                  <Link to={`/governance/applications/${a.id}`} className="font-medium">
                    {a.name}
                  </Link>
                  <span className="text-slate-500">
                    {connectorLabels[a.connector] ?? a.connector}
                  </span>
                </li>
              ))}
            </ul>
          )}
        </Card>
      </section>
      <section aria-labelledby="gallery">
        <h2 id="gallery" className="mb-2 text-sm font-semibold text-slate-700">
          Connect an application
        </h2>
        <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
          {connectorTypes.map((t) => (
            <Card key={t.name} className="flex flex-col p-4">
              <div className="flex items-start gap-3">
                <span
                  className="flex size-10 shrink-0 items-center justify-center rounded-md bg-brand-50 text-sm font-semibold text-brand-700"
                  aria-hidden
                >
                  {t.mark}
                </span>
                <div className="min-w-0">
                  <div className="flex flex-wrap items-center gap-2 font-medium">
                    {t.name}
                    {t.hrSource && <Badge>HR source</Badge>}
                  </div>
                  <p className="text-sm text-slate-500">{t.blurb}</p>
                </div>
              </div>
              <div className="mt-auto pt-3">
                {t.hrSource ? (
                  <Link to="/settings/hr-sources" className="text-sm text-brand-600">
                    Connect it from HR sources
                  </Link>
                ) : (
                  <Manage>
                    <Link
                      to={`/governance/applications/new?connector=${t.kind}`}
                      className={buttonVariants({ size: "sm", variant: "outline" })}
                      aria-label={`Connect ${t.name}`}
                    >
                      Connect
                    </Link>
                  </Manage>
                )}
              </div>
            </Card>
          ))}
        </div>
      </section>
    </div>
  );
}
