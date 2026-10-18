import type { CSSProperties } from "react";

// Space kept between the explanation and the edges of the screen.
const margin = 16;
const gap = 8;
const maxWidth = 416;
// Below the "?" unless less than this fits there and more fits above.
const minBelow = 320;

/**
 * Where the explanation goes: next to the "?", always inside the screen.
 * It opens below, or above when the "?" is near the bottom, and is never
 * taller than the space there: longer content scrolls inside it.
 */
export function placement(anchor: DOMRect, align: "start" | "end", vw: number, vh: number) {
  const width = Math.min(maxWidth, vw - 2 * margin);
  const wanted = align === "end" ? anchor.right - width : anchor.left;
  const left = Math.max(margin, Math.min(wanted, vw - width - margin));
  const below = vh - anchor.bottom - gap - margin;
  const above = anchor.top - gap - margin;
  const style: CSSProperties = { left, width };
  if (below >= minBelow || below >= above) {
    style.top = anchor.bottom + gap;
    style.maxHeight = Math.max(below, 0);
  } else {
    style.bottom = vh - anchor.top + gap;
    style.maxHeight = above;
  }
  return style;
}
