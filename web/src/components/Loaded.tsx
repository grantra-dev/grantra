import type { ReactNode } from "react";
import { Card } from "@/components/ui/card";

/** A card that shows loading, an error or an empty message before its content. */
export function Loaded({
  loading,
  error,
  empty,
  children,
}: {
  loading: boolean;
  error: Error | null;
  empty?: string | false;
  children: ReactNode;
}) {
  return (
    <Card>
      {loading ? (
        <p className="p-6 text-sm text-slate-500">Loading…</p>
      ) : error ? (
        <p role="alert" className="p-6 text-sm text-red-700">
          Could not load this: {error.message}
        </p>
      ) : empty ? (
        <p className="p-6 text-sm text-slate-500">{empty}</p>
      ) : (
        children
      )}
    </Card>
  );
}
