import { Button } from "@/components/ui/button";
import { cn } from "@/lib/utils";

/** The button under a paged list that loads its next page, while there is one. */
export function ShowMore({
  query,
  label = "Show more",
  className,
}: {
  query: { hasNextPage: boolean; isFetchingNextPage: boolean; fetchNextPage: () => unknown };
  label?: string;
  className?: string;
}) {
  if (!query.hasNextPage) return null;
  return (
    <div className={cn("border-t border-slate-200 p-3 text-center", className)}>
      <Button
        variant="outline"
        onClick={() => void query.fetchNextPage()}
        disabled={query.isFetchingNextPage}
      >
        {query.isFetchingNextPage ? "Loading…" : label}
      </Button>
    </div>
  );
}
