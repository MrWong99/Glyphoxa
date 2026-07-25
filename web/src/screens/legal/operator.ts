// Operator identity for the legal pages (#518).
//
// §5 DDG (ex-§5 TMG) requires a German-operated service to name its operator:
// legal name, postal address, and a contact that reaches a human. That
// information belongs to the OPERATOR of a deployment — it is not something the
// chart, the code, or an agent may invent — so it lives here as a committed,
// clearly-placeholdered file the operator fills in before the deployment goes
// live. See docs/deploy/legal-pages.md.
//
// Anything still holding a PLACEHOLDER value renders as a visible red
// "operator TODO" marker on the page (see isPlaceholder / OperatorValue), so an
// unfilled Impressum is impossible to miss rather than quietly wrong.
//
// The legal texts these values feed are TEMPLATES drafted from established
// German boilerplate, not legal advice, and they have not been reviewed by a
// lawyer (an accepted beta risk, decided 2026-07-22 — see #518).

/** The marker every unfilled field carries. */
export const PLACEHOLDER_PREFIX = "[BITTE AUSFÜLLEN";

export type OperatorIdentity = {
  /** Legal name of the natural or legal person operating this deployment. */
  legalName: string;
  /** Street and number. */
  street: string;
  /** Postal code and city. */
  city: string;
  /** Country. */
  country: string;
  /** An email address that reaches a human (§5 DDG requires a fast contact). */
  email: string;
  /** Optional: phone number. Leave empty to omit the line. */
  phone: string;
  /**
   * Responsible for editorial content under §18 Abs. 2 MStV. Usually the same
   * person as legalName — leave empty to omit the section.
   */
  contentResponsible: string;
  /** Optional: VAT id (§27a UStG). Leave empty to omit the line. */
  vatId: string;
  /**
   * Optional: the Datenschutzbeauftragte(r) (DPO). Most beta-scale operators do
   * not need one (Art. 37 GDPR) — leave empty to omit the section.
   */
  dataProtectionOfficer: string;
  /** The supervisory authority a data subject may complain to (Art. 77 GDPR). */
  supervisoryAuthority: string;
  /** Where the servers physically run — the residency claim must be true. */
  hostingLocation: string;
  /** Last review date of the legal texts, shown at the top of each page. */
  lastUpdated: string;
};

/**
 * OPERATOR is the deployment's own identity. EVERY placeholder below must be
 * replaced before the deployment is reachable from the internet.
 */
export const OPERATOR: OperatorIdentity = {
  legalName: "[BITTE AUSFÜLLEN: vollständiger Name des Betreibers]",
  street: "[BITTE AUSFÜLLEN: Straße und Hausnummer]",
  city: "[BITTE AUSFÜLLEN: PLZ und Ort]",
  country: "Deutschland",
  email: "[BITTE AUSFÜLLEN: Kontakt-E-Mail-Adresse]",
  phone: "",
  contentResponsible: "",
  vatId: "",
  dataProtectionOfficer: "",
  supervisoryAuthority:
    "[BITTE AUSFÜLLEN: zuständige Landesdatenschutzbehörde, z. B. „Der Landesbeauftragte für den Datenschutz und die Informationsfreiheit Baden-Württemberg“]",
  hostingLocation: "[BITTE AUSFÜLLEN: Standort der Server, z. B. „Deutschland (eigener Server)“]",
  lastUpdated: "[BITTE AUSFÜLLEN: Datum der letzten Überarbeitung]",
};

/** isPlaceholder reports whether a field is still an unfilled template value. */
export function isPlaceholder(value: string): boolean {
  return value.trim().startsWith(PLACEHOLDER_PREFIX);
}

/**
 * operatorTodos lists the fields an operator still has to fill. Empty means the
 * identity is complete; the legal pages render a banner while it is not.
 * Optional fields (phone, VAT id, DPO, editorial responsibility) are excluded —
 * an empty optional field is a decision, not an omission.
 */
export function operatorTodos(o: OperatorIdentity = OPERATOR): string[] {
  const required: Array<keyof OperatorIdentity> = [
    "legalName",
    "street",
    "city",
    "country",
    "email",
    "supervisoryAuthority",
    "hostingLocation",
    "lastUpdated",
  ];
  return required.filter((k) => isPlaceholder(o[k]) || o[k].trim() === "");
}
