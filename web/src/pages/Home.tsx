import { ArrowRight } from "lucide-react";
import { Link } from "react-router";
import { PageHeader } from "@/components/PageHeader";
import { Card, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import type { Me } from "@/lib/api";
import { visibleGroups } from "@/modules";

export function Home({ me }: { me: Me }) {
  const firstName = me.display_name.split(" ")[0];
  return (
    <>
      <PageHeader title={`Hi, ${firstName}`} description="What would you like to do?" />
      <div className="space-y-8">
        {visibleGroups(me).map((g) => (
          <section key={g.id} aria-labelledby={`home-${g.id}`}>
            <h2
              id={`home-${g.id}`}
              className="mb-3 text-xs font-semibold tracking-wide text-slate-500 uppercase"
            >
              {g.title}
            </h2>
            <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
              {g.items.map((s) => (
                <Link key={s.path} to={s.path} className="group">
                  <Card className="h-full transition-colors group-hover:border-brand-600">
                    <CardHeader>
                      <s.icon className="mb-2 size-5 text-brand-600" aria-hidden />
                      <CardTitle className="flex items-center justify-between">
                        {s.title}
                        <ArrowRight
                          className="size-4 text-slate-400 group-hover:text-brand-600"
                          aria-hidden
                        />
                      </CardTitle>
                      <CardDescription>{s.description}</CardDescription>
                    </CardHeader>
                  </Card>
                </Link>
              ))}
            </div>
          </section>
        ))}
      </div>
    </>
  );
}
