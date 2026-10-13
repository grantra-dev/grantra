import type { ComponentProps } from "react";
import { cn } from "@/lib/utils";

export function Table({ className, ...props }: ComponentProps<"table">) {
  return (
    <div className="overflow-x-auto">
      <table className={cn("w-full text-left text-sm", className)} {...props} />
    </div>
  );
}

export function TableHeader(props: ComponentProps<"thead">) {
  return <thead className="border-b border-slate-200 text-slate-500" {...props} />;
}

export function TableBody(props: ComponentProps<"tbody">) {
  return <tbody className="divide-y divide-slate-100" {...props} />;
}

export function TableRow(props: ComponentProps<"tr">) {
  return <tr {...props} />;
}

export function TableHead({ className, ...props }: ComponentProps<"th">) {
  return <th className={cn("px-4 py-2 font-medium", className)} {...props} />;
}

export function TableCell({ className, ...props }: ComponentProps<"td">) {
  return <td className={cn("px-4 py-3 align-top", className)} {...props} />;
}
