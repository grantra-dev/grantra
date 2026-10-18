import { CircleHelp, X } from "lucide-react";
import { useEffect, useId, useRef, useState, type ReactNode } from "react";

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
  /** Which edge of the "?" the explanation lines up with, from sm up. */
  align?: "start" | "end";
  children: ReactNode;
}) {
  const [open, setOpen] = useState(false);
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

  return (
    <div ref={root} className="relative inline-flex">
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
        // On a phone it spans the screen; from sm up it hangs below the "?".
        <div
          id={id}
          role="dialog"
          aria-label={label}
          className={`fixed inset-x-4 top-20 z-30 max-h-[75vh] overflow-y-auto rounded-lg border border-slate-200 bg-white p-4 text-left text-sm font-normal text-slate-700 shadow-lg sm:absolute sm:inset-x-auto sm:top-full sm:mt-2 sm:w-[26rem] ${align === "end" ? "sm:right-0" : "sm:left-0"}`}
        >
          <button
            type="button"
            aria-label="Close"
            onClick={() => {
              setOpen(false);
              button.current?.focus();
            }}
            className="absolute top-2 right-2 inline-flex size-7 items-center justify-center rounded-md text-slate-400 hover:bg-slate-100 hover:text-slate-700"
          >
            <X className="size-4" aria-hidden />
          </button>
          {children}
        </div>
      )}
    </div>
  );
}
