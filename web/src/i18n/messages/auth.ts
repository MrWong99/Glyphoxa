// Filled by the per-area localization pass. `en` is the key source of truth;
// `de` must cover every key (compile-checked).

export const en = {} as const;

export const de: Record<keyof typeof en, string> = {};
