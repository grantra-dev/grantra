import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { Loaded } from "@/components/Loaded";
import { Alert } from "@/components/ui/alert";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input, Label } from "@/components/ui/field";
import { type Me } from "@/lib/api";
import { focusRing, useFocusParam } from "@/lib/focus";
import { cn, formatTime } from "@/lib/utils";
import { listBreakGlassUses, reviewBreakGlass, type VaultCheckout } from "@/lib/vault";

const reviewBadges = {
  pending: { variant: "amber", label: "To review" },
  justified: { variant: "green", label: "Justified" },
  flagged: { variant: "red", label: "Flagged" },
} as const;

/** Emergency uses of the vault, and their review. */
export function BreakGlassUses({ me, admin }: { me: Me; admin: boolean }) {
  const [all, setAll] = useState(false);
  const { data, error, isPending } = useQuery({
    queryKey: ["break-glass", all],
    queryFn: () => listBreakGlassUses(all),
  });
  // The inbox links here with ?use=<id>.
  const asked = useFocusParam("use", !!data);
  return (
    <section className="flex flex-col gap-3">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <p className="text-sm text-slate-500">
          Every time someone breaks the glass, an admin other than them confirms it was justified or
          flags it. Admins are reminded daily until then.
        </p>
        <label className="flex items-center gap-2 text-sm">
          <input type="checkbox" checked={all} onChange={(e) => setAll(e.target.checked)} />
          Show reviewed uses too
        </label>
      </div>
      <Loaded
        loading={isPending}
        error={error}
        empty={
          data?.length === 0 && (all ? "Nobody has broken the glass yet." : "Nothing to review.")
        }
      >
        <ul className="divide-y divide-slate-200">
          {data?.map((c) => (
            <BreakGlassUse
              key={c.id}
              c={c}
              focused={asked === c.id}
              canReview={admin && c.identity_id !== me.identity_id}
            />
          ))}
        </ul>
      </Loaded>
    </section>
  );
}

function BreakGlassUse({
  c,
  focused,
  canReview,
}: {
  c: VaultCheckout;
  focused: boolean;
  canReview: boolean;
}) {
  const client = useQueryClient();
  const [comment, setComment] = useState("");
  const review = useMutation({
    mutationFn: (justified: boolean) => reviewBreakGlass(c.id, justified, comment.trim()),
    onSuccess: () =>
      Promise.all([
        client.invalidateQueries({ queryKey: ["break-glass"] }),
        client.invalidateQueries({ queryKey: ["inbox"] }),
      ]),
  });
  const badge = reviewBadges[c.review_status ?? "pending"];
  return (
    <li data-focus={c.id} className={cn("p-4", focused && focusRing)}>
      <div className="flex flex-wrap items-center gap-2">
        <span className="font-medium">
          {c.identity_name} opened {c.account_name}
        </span>
        <Badge variant={badge.variant}>{badge.label}</Badge>
      </div>
      <div className="text-sm text-slate-500">
        {formatTime(c.checked_out_at)}
        {c.ended_at
          ? ` until ${formatTime(c.ended_at)}`
          : ` · open until ${formatTime(c.expires_at)}`}
      </div>
      <p className="mt-1 text-sm text-slate-700">“{c.reason}”</p>
      {c.review_status !== "pending" && c.reviewed_at && (
        <p className="mt-1 text-sm text-slate-500">
          Reviewed {formatTime(c.reviewed_at)}
          {c.review_comment && `: ${c.review_comment}`}
        </p>
      )}
      {c.review_status === "pending" && canReview && (
        <div className="mt-2 flex flex-wrap items-end gap-2">
          <div className="min-w-60 flex-1">
            <Label htmlFor={`review-${c.id}`}>Comment (needed to flag)</Label>
            <Input
              id={`review-${c.id}`}
              value={comment}
              onChange={(e) => setComment(e.target.value)}
              placeholder="Matches INC-1234"
            />
          </div>
          <Button size="sm" disabled={review.isPending} onClick={() => review.mutate(true)}>
            Justified
          </Button>
          <Button
            size="sm"
            variant="destructive"
            disabled={review.isPending || !comment.trim()}
            onClick={() => review.mutate(false)}
          >
            Flag it
          </Button>
        </div>
      )}
      {review.error && (
        <Alert variant="error" className="mt-2">
          {review.error.message}
        </Alert>
      )}
    </li>
  );
}
