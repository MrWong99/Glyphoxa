import type { MouseEvent, ReactNode } from "react";
import { Check, Circle, CircleDashed, ChevronRight } from "lucide-react";

import { useScreenNav } from "@/app/screenNav";
import { Alert } from "@/components/ui/Alert";
import { Button } from "@/components/ui/Button";
import { Card } from "@/components/ui/Card";
import { useI18n } from "@/i18n";
import { isReady, type SetupOptionalKey, type SetupStep } from "@/lib/readiness";

// SetupChecklist opens Setup (#695): the four things a first Voice Session
// needs, in order, each ticked from the reads the page already makes
// (deriveSetupSteps), each linking to the section below where it is done. A
// step whose read has not answered shows as "checking", never as a to-do.
// When all four are known to be done the list leads with a success banner
// and "Go to Session". The optional extras are listed apart, so they never
// read as something the table is missing.

/**
 * Moves to a Setup section without touching the URL hash (the router owns
 * the URL): scroll it into view and put keyboard focus on it, so the next
 * Tab continues from there.
 */
export function jumpTo(anchor: string, e?: MouseEvent) {
  e?.preventDefault();
  const target = document.getElementById(anchor);
  if (!target) return;
  target.scrollIntoView?.({ behavior: "smooth", block: "start" });
  target.focus({ preventScroll: true });
}

export function SetupChecklist({
  steps,
  optional,
  details,
  bodies,
  go,
}: {
  steps: SetupStep[];
  optional: SetupStep<SetupOptionalKey>[];
  /** A short "what is set" line per done step (the campaign's name, the server's). */
  details?: Partial<Record<string, string>>;
  /** A step's inline body (step 1's create-a-campaign form). */
  bodies?: Partial<Record<string, ReactNode>>;
  /** Where a step's link goes instead of its section (a done campaign: the Campaigns page). */
  go?: Partial<Record<string, () => void>>;
}) {
  const { t } = useI18n();
  const { goScreen } = useScreenNav();
  const ready = isReady(steps);
  const known = steps.filter((s) => s.done !== undefined);
  const done = known.filter((s) => s.done).length;

  return (
    <Card className="gx-setup-checklist" aria-labelledby="setup-checklist-title">
      <div className="gx-setup-checklist__head">
        <h2 id="setup-checklist-title" className="gx-setup-checklist__title">
          {t("config.checklistTitle")}
        </h2>
        <span className="gx-setup-checklist__progress">
          {t("config.checklistProgress", { done, total: steps.length })}
        </span>
      </div>

      {ready && (
        <Alert
          tone="success"
          title={t("config.readyTitle")}
          action={
            <Button size="sm" variant="primary" onClick={() => goScreen("session")}>
              {t("config.goToSession")}
            </Button>
          }
        >
          {t("config.readyLede")}
        </Alert>
      )}

      <ol className="gx-setup-steps">
        {steps.map((step, i) => (
          <StepItem
            key={step.key}
            step={step}
            number={i + 1}
            detail={details?.[step.key]}
            body={bodies?.[step.key]}
            go={go?.[step.key]}
          />
        ))}
      </ol>

      <h3 className="gx-setup-checklist__optional">{t("config.optionalTitle")}</h3>
      <ul className="gx-setup-steps gx-setup-steps--optional">
        {optional.map((item) => (
          <StepItem key={item.key} step={item} />
        ))}
      </ul>
    </Card>
  );
}

function StepItem({
  step,
  number,
  detail,
  body,
  go,
}: {
  step: SetupStep<string>;
  number?: number;
  detail?: string;
  body?: ReactNode;
  go?: () => void;
}) {
  const { t } = useI18n();
  const state = step.done === true ? "done" : step.done === false ? "todo" : "checking";
  const Mark = state === "done" ? Check : state === "todo" ? Circle : CircleDashed;
  const status = t(
    state === "done" ? "config.stepStatusDone" : state === "todo" ? "config.stepStatusTodo" : "config.stepStatusChecking",
  );
  const label = (
    <>
      {number !== undefined && <span className="gx-setup-step__num">{number}.</span>} {t(step.label)}
    </>
  );

  return (
    <li id={body ? step.anchor : undefined} tabIndex={body ? -1 : undefined} className="gx-setup-step" data-state={state}>
      <span className="gx-setup-step__mark" aria-hidden="true">
        <Mark />
      </span>
      <div className="gx-setup-step__text">
        {/* A step with its body inline needs no link: it is done right here. */}
        {body ? (
          <span className="gx-setup-step__label">{label}</span>
        ) : (
          <a
            className="gx-setup-step__label gx-setup-step__link"
            href={`#${step.anchor}`}
            onClick={(e) => {
              if (!go) return jumpTo(step.anchor, e);
              e.preventDefault();
              go();
            }}
          >
            {label}
            <ChevronRight className="gx-setup-step__chevron" aria-hidden="true" />
          </a>
        )}
        <span className="gx-visually-hidden">{status}</span>
        {state === "done" && detail ? (
          <span className="gx-setup-step__hint">{detail}</span>
        ) : (
          state !== "done" && <span className="gx-setup-step__hint">{t(step.hint)}</span>
        )}
        {body && state !== "done" && <div className="gx-setup-step__body">{body}</div>}
      </div>
    </li>
  );
}
