import { OPERATOR, isPlaceholder } from "@/screens/legal/operator";

/**
 * operatorHref is how Setup reaches the operator for what a table cannot do
 * itself (delete the table, change its plan): a mail with the given subject
 * when the deployment has filled in its address, else the Impressum page,
 * which names the operator however they can be reached.
 */
export function operatorHref(subject: string): string {
  if (!OPERATOR.email || isPlaceholder(OPERATOR.email)) return "/imprint";
  return `mailto:${OPERATOR.email}?subject=${encodeURIComponent(subject)}`;
}
