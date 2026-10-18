import { PageHeader } from "@/components/PageHeader";
import { Card, CardContent } from "@/components/ui/card";
import type { NavItem } from "@/modules";

export function ComingSoon({ section }: { section: NavItem }) {
  return (
    <>
      <PageHeader title={section.title} description={section.description} />
      <Card>
        <CardContent className="pt-5 text-sm text-slate-500">
          This page is coming in the next console release
          {section.issue && (
            <>
              {" "}
              (
              <a
                className="text-brand-600 underline"
                href={`https://github.com/grantra-dev/grantra/issues/${section.issue}`}
              >
                #{section.issue}
              </a>
              )
            </>
          )}
          . The API behind it already works.
        </CardContent>
      </Card>
    </>
  );
}
