import { ArrowLeft, ArrowRight } from "lucide-react";
import { useEffect, useMemo } from "react";
import { Link, useLocation, useParams } from "react-router";
import { Markdown } from "@/components/Markdown";
import { PageHeader } from "@/components/PageHeader";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import type { Me } from "@/lib/api";
import { parseMarkdown } from "@/lib/markdown";
import { visibleHelpPages } from "@/help/pages";
import { NotFound } from "./NotFound";

export function Help({ me }: { me: Me }) {
  return (
    <>
      <PageHeader title="Help" description="How Grantra works, for what you do in it." />
      <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
        {visibleHelpPages(me).map((p) => (
          <Link key={p.slug} to={`/help/${p.slug}`} className="group">
            <Card className="h-full transition-colors group-hover:border-brand-600">
              <CardHeader>
                <CardTitle className="flex items-center justify-between">
                  {p.title}
                  <ArrowRight
                    className="size-4 text-slate-400 group-hover:text-brand-600"
                    aria-hidden
                  />
                </CardTitle>
                <CardDescription>{p.summary}</CardDescription>
              </CardHeader>
            </Card>
          </Link>
        ))}
      </div>
    </>
  );
}

export function HelpArticle({ me }: { me: Me }) {
  const { page: slug } = useParams();
  const { hash } = useLocation();
  const pages = useMemo(() => visibleHelpPages(me), [me]);
  const page = pages.find((p) => p.slug === slug);
  const blocks = useMemo(() => (page ? parseMarkdown(page.body) : []), [page]);
  const slugs = useMemo(() => new Set(pages.map((p) => p.slug)), [pages]);

  // Links like /help/csv-files#when-a-file-is-refused open at the heading.
  useEffect(() => {
    if (hash) document.getElementById(decodeURIComponent(hash.slice(1)))?.scrollIntoView();
    else window.scrollTo(0, 0);
  }, [hash, slug]);

  if (!page) return <NotFound />;
  return (
    <>
      <Link
        to="/help"
        className="mb-4 inline-flex items-center gap-1 text-sm text-slate-500 hover:text-slate-900"
      >
        <ArrowLeft className="size-4" aria-hidden />
        Help
      </Link>
      <PageHeader title={page.title} description={page.summary} />
      <Card>
        <CardContent className="pt-5">
          <article aria-label={page.title}>
            <Markdown blocks={blocks} pages={slugs} />
          </article>
        </CardContent>
      </Card>
    </>
  );
}
