import type { ReactNode } from "react";
import { Button } from "@/components/ui/button";
import { ErrorAlert } from "@/components/ui/alert";
import { cn } from "@/lib/utils";

/**
 * The end of a form: why saving failed, then the submit button, any other
 * buttons, and Cancel. Submit waits while the change runs.
 */
export function FormActions({
  action,
  submit,
  disabled,
  onCancel,
  className,
  children,
}: {
  /** The change the form makes, such as a useMutation result. */
  action: { error: Error | null; isPending: boolean };
  submit: ReactNode;
  /** The form is not ready to send. */
  disabled?: boolean;
  onCancel?: () => void;
  /** Classes for both rows, such as a grid column span. */
  className?: string;
  /** Buttons between submit and Cancel. */
  children?: ReactNode;
}) {
  return (
    <>
      <ErrorAlert error={action.error} className={className} />
      <div className={cn("flex gap-2", className)}>
        <Button type="submit" disabled={disabled || action.isPending}>
          {submit}
        </Button>
        {children}
        {onCancel && (
          <Button type="button" variant="ghost" onClick={onCancel}>
            Cancel
          </Button>
        )}
      </div>
    </>
  );
}
