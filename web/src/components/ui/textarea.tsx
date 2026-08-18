import type * as React from "react";

import { cn } from "@/lib/utils";

/**
 * The console's multi-line box, defaulted to no autofill for the same reason as
 * Input above: nothing anybody types into this console is a credential, an
 * address or a card number, so the browser has nothing useful to offer and
 * guesses instead. A caller that wants otherwise passes its own `autoComplete`.
 */
export function Textarea({
  className,
  ref,
  autoComplete = "off",
  ...props
}: React.TextareaHTMLAttributes<HTMLTextAreaElement> & {
  ref?: React.Ref<HTMLTextAreaElement>;
}) {
  return (
    <textarea
      ref={ref}
      autoComplete={autoComplete}
      className={cn(
        "flex min-h-[72px] w-full resize-none rounded-md border border-input bg-background px-3 py-2 text-sm shadow-sm placeholder:text-muted-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring disabled:cursor-not-allowed disabled:opacity-50",
        className,
      )}
      {...props}
    />
  );
}
