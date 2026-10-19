import type { ReviewItem } from "./api";
import { hasWords } from "./utils";

/** What a reviewer narrows a long campaign down to. */
export interface ReviewFilter {
  campaign?: string | null;
  /** Words to find in the person, the access or its application. */
  text?: string;
  risk?: string;
  origin?: string;
  /** What Grantra suggests: "keep", "revoke", or "none" for nothing to say. */
  suggestion?: string;
}

export type ReviewSort = "person" | "access" | "risk" | "oldest";

const riskRank: Record<string, number> = { critical: 0, high: 1, medium: 2, low: 3 };

/** The items that match every part of the filter. */
export function filterReviewItems(items: ReviewItem[], f: ReviewFilter): ReviewItem[] {
  return items.filter((it) => {
    if (f.campaign && it.campaign_id !== f.campaign) return false;
    if (f.risk && (it.risk ?? "") !== f.risk) return false;
    if (f.origin && it.origin !== f.origin) return false;
    if (f.suggestion && (it.suggestion?.action ?? "none") !== f.suggestion) return false;
    return hasWords(f.text ?? "", it.identity_name, it.target_name, it.application);
  });
}

/** The items in the order asked for; ties keep a stable person, access order. */
export function sortReviewItems(items: ReviewItem[], by: ReviewSort): ReviewItem[] {
  const name = (a: ReviewItem, b: ReviewItem) =>
    a.identity_name.localeCompare(b.identity_name) || a.target_name.localeCompare(b.target_name);
  const compare: Record<ReviewSort, (a: ReviewItem, b: ReviewItem) => number> = {
    person: name,
    access: (a, b) => a.target_name.localeCompare(b.target_name) || name(a, b),
    risk: (a, b) => (riskRank[a.risk ?? ""] ?? 4) - (riskRank[b.risk ?? ""] ?? 4) || name(a, b),
    oldest: (a, b) => a.granted_at.localeCompare(b.granted_at) || name(a, b),
  };
  return [...items].sort(compare[by]);
}
