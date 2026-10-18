import { CircleHelp, X } from "lucide-react";
import {
  useEffect,
  useId,
  useLayoutEffect,
  useRef,
  useState,
  type CSSProperties,
  type ReactNode,
} from "react";
import { placement } from "@/lib/placement";

/**
 * A "?" that opens a short explanation next to what it explains. It closes
 * on Escape, on a click elsewhere, with its own close button, or on the "?"
 * again.
 */
export function HelpTip({
  label,
  align = "start",
  children,
}: {
  label: string;
  /** Which edge of the "?" the explanation lines up with, when it fits. */
  align?: "start" | "end";
  children: ReactNode;
}) {
  const [open, setOpen] = useState(false);
  const [style, setStyle] = useState<CSSProperties>();
  const root = useRef<HTMLDivElement>(null);
  const button = useRef<HTMLButtonElement>(null);
  const id = useId();

  useEffect(() => {
    if (!open) return;
    const onPointer = (e: PointerEvent) => {
      if (!root.current?.contains(e.target as Node)) setOpen(false);
    };
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") {
        setOpen(false);
        button.current?.focus();
      }
    };
    document.addEventListener("pointerdown", onPointer);
    document.addEventListener("keydown", onKey);
    return () => {
      document.removeEventListener("pointerdown", onPointer);
      document.removeEventListener("keydown", onKey);
    };
  }, [open]);

  // Placed before the browser paints, and again when the page scrolls or
  // the window changes size, so it follows the "?".
  useLayoutEffect(() => {
    if (!open) return;
    const place = () => {
      const anchor = button.current?.getBoundingClientRect();
      if (anchor) setStyle(placement(anchor, align, window.innerWidth, window.innerHeight));
    };
    place();
    window.addEventListener("resize", place);
    window.addEventListener("scroll", place, true);
    return () => {
      window.removeEventListener("resize", place);
      window.removeEventListener("scroll", place, true);
    };
  }, [open, align]);

  return (
    <div ref={root} className="inline-flex">
      <button
        ref={button}
        type="button"
        aria-label={label}
        title={label}
        aria-expanded={open}
        aria-controls={open ? id : undefined}
        onClick={() => setOpen((o) => !o)}
        className="inline-flex size-8 items-center justify-center rounded-full text-slate-500 hover:bg-slate-100 hover:text-brand-700 focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-brand-600"
      >
        <CircleHelp className="size-5" aria-hidden />
      </button>
      {open && (
        <div
          id={id}
          role="dialog"
          aria-label={label}
          style={style}
          className="fixed z-30 flex flex-col overflow-hidden rounded-lg border border-slate-200 bg-white text-left text-sm font-normal text-slate-700 shadow-lg"
        >
          <button
            type="button"
            aria-label="Close"
            onClick={() => {
              setOpen(false);
              button.current?.focus();
            }}
            className="absolute top-2 right-2 z-10 inline-flex size-7 items-center justify-center rounded-md bg-white text-slate-400 hover:bg-slate-100 hover:text-slate-700"
          >
            <X className="size-4" aria-hidden />
          </button>
          {/* Only the content scrolls, so the close button stays in view. */}
          <div className="overflow-y-auto overscroll-contain p-4">{children}</div>
        </div>
      )}
    </div>
  );
}
