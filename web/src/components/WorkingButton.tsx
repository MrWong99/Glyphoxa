import { forwardRef, type ComponentProps } from "react";

import { Button } from "@/components/ui/Button";
import { Spinner } from "@/components/ui/Spinner";

import "./workingButton.css";

// WorkingButton — a Button for an action that takes a while (packing a bundle,
// a create-then-switch) and whose pending state must not cost the keyboard its
// place.
//
// Button's own `loading` sets the native `disabled`, and a browser takes focus
// off a control the moment it becomes disabled: a GM who pressed Save with the
// keyboard is on <body> by the time the save answers, and has to find the form
// again to act on a failure. While `working`, this button stays focusable and
// announces itself busy (aria-busy) instead; a click or Enter is swallowed, so
// nothing is sent twice, and a spinner takes the place of its icon.
//
// The caller changes the label for the pending state ("Packing…") — the label
// is what a screen reader hears when the button is busy.
export const WorkingButton = forwardRef<
  HTMLButtonElement,
  ComponentProps<typeof Button> & { working: boolean }
>(function WorkingButton({ working, onClick, iconStart, className = "", ...props }, ref) {
  return (
    <Button
      ref={ref}
      {...props}
      className={working ? `${className} gx-btn--working`.trim() : className}
      aria-busy={working || undefined}
      iconStart={working ? <Spinner /> : iconStart}
      onClick={(event) => {
        if (working) {
          // Also stops a type="submit" button submitting its form again.
          event.preventDefault();
          return;
        }
        onClick?.(event);
      }}
    />
  );
});
