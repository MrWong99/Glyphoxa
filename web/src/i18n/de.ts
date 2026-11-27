// The German catalog, assembled from the per-area fragments — the ONLY module
// that references a fragment's `de` export, and only ever reached through the
// dynamic import in ./index.tsx, so it ships as its own chunk and an English
// visitor never downloads it.
//
// The `?de` suffix is what makes that split real. Each fragment file exports
// BOTH `en` and `de`, and a bundler places a module in one chunk as a whole:
// importing "./messages/auth" here would drag the German strings into the
// entry bundle (where the English half already lives). A query suffix makes
// the bundler treat the same source file as a second, distinct module; tree-
// shaking then drops `en` from this copy and `de` from the entry's copy. The
// fragment files stay one-file-per-area with both languages side by side —
// which is what keeps a missing German string a compile error in THAT file.
//
// Types: "*?de" is declared in ./de-query.d.ts as a plain string map. The
// per-fragment `Record<keyof typeof en, string>` annotation is still what
// guarantees completeness; i18n.test.tsx additionally checks the assembled
// catalog key-for-key against English.
import { de as access } from "./messages/access?de";
import { de as auth } from "./messages/auth?de";
import { de as campaign } from "./messages/campaign?de";
import { de as campaigns } from "./messages/campaigns?de";
import { de as cast } from "./messages/cast?de";
import { de as chat } from "./messages/chat?de";
import { de as common } from "./messages/common?de";
import { de as components } from "./messages/components?de";
import { de as configuration } from "./messages/configuration?de";
import { de as home } from "./messages/home?de";
import { de as knowledge } from "./messages/knowledge?de";
import { de as maps } from "./messages/maps?de";
import { de as palette } from "./messages/palette?de";
import { de as session } from "./messages/session?de";
import { de as shell } from "./messages/shell?de";

export const de: Record<string, string> = {
  ...common,
  ...shell,
  ...components,
  ...configuration,
  ...campaign,
  ...campaigns,
  ...home,
  ...cast,
  ...maps,
  ...chat,
  ...knowledge,
  ...palette,
  ...session,
  ...auth,
  ...access,
};
