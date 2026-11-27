import { en, type MsgParams, type TFunc } from "@/i18n";
import { de as sessionDe } from "@/i18n/messages/session";

// Test-only: plain translate functions for the Session screen's pure helpers
// (failureText, speakers, startStopError, transcriptMarkdown), which take `t` as an argument
// rather than reading it from React. tDe covers the session catalog — the only
// one those helpers use — and falls back to English like the app does.

function format(template: string, params?: MsgParams): string {
  if (!params) return template;
  return template.replace(/\{(\w+)\}/g, (match, name: string) =>
    name in params ? String(params[name]) : match,
  );
}

export const tEn: TFunc = (key, params) => format(en[key], params);

export const tDe: TFunc = (key, params) =>
  format((sessionDe as Record<string, string>)[key] ?? en[key], params);
