import type * as React from "react";

import { cn } from "@/lib/utils";

/**
 * The console's text field.
 *
 * `type` and `autoComplete` are both defaulted here rather than left to the
 * caller. A browser decides what a field is for by guessing, and the two things
 * it guesses from are the type and the autocomplete token: an input with
 * neither, sitting alone in a form with a submit button, is the exact shape of
 * a sign-in box, so Chrome offers a saved password or a stored card over it.
 * That is what the raise-a-todo box in the room panel looked like, and the
 * operator got payment suggestions while writing down work.
 *
 * So the default is "this field is not one of yours". A caller that genuinely
 * wants the browser to fill something in passes its own `autoComplete` and wins,
 * because props are spread after these - but it has to ask, which is the right
 * way round. Defaulting it at the component means the next field somebody adds
 * is safe without anybody remembering this.
 */
export function Input({
  className,
  type = "text",
  autoComplete = "off",
  ...props
}: React.InputHTMLAttributes<HTMLInputElement>) {
  return (
    <input
      type={type}
      autoComplete={autoComplete}
      className={cn(
        "flex h-9 w-full rounded-md border border-input bg-background px-3 py-1 text-sm shadow-sm transition-colors placeholder:text-muted-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring disabled:cursor-not-allowed disabled:opacity-50",
        className,
      )}
      {...props}
    />
  );
}
