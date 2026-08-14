import type * as React from "react";

import { cn } from "@/lib/utils";

/**
 * A select, styled like the rest of the kit.
 *
 * It is the platform's own rather than a listbox built out of divs: the
 * lifecycle dropdown has seven options at most, it has to work under a keyboard
 * and inside a jsdom the gate mounts the console in, and none of that is worth
 * a dependency.
 */
export function Select({ className, ...props }: React.SelectHTMLAttributes<HTMLSelectElement>) {
  return (
    <select
      className={cn(
        "h-8 rounded-md border border-border bg-transparent px-2 text-sm",
        "focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring",
        "disabled:cursor-not-allowed disabled:opacity-50",
        className,
      )}
      {...props}
    />
  );
}
