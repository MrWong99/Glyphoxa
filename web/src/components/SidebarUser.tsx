import { useState } from "react";
import * as RMenu from "@radix-ui/react-dropdown-menu";
import { useMutation } from "@connectrpc/connect-query";
import { useQueryClient } from "@tanstack/react-query";
import { useNavigate } from "@tanstack/react-router";
import { Check, ChevronsUpDown, ExternalLink, LogOut } from "lucide-react";
import { toast } from "sonner";

import { AuthService } from "@gen/glyphoxa/management/v1/management_pb";
import type { User } from "@gen/glyphoxa/management/v1/management_pb";

import { Avatar } from "./ui/Avatar";
import { ConfirmDialog } from "./ui/ConfirmDialog";
import { Menu, MenuItem, MenuLabel, MenuSeparator } from "./ui/Menu";
import { LANGS, LANG_NAMES, useI18n, type Lang, type MessageKey, type TFunc } from "@/i18n";
import { errorMessage } from "@/lib/connectError";
import { useCan } from "@/app/capabilities";
import { useLiveSession } from "@/lib/useLiveSession";

// SidebarUser is the app-shell footer identity (ADR-0016 / ADR-0039) and the
// account menu behind it. The row shows the real signed-in user the AuthGate
// resolved — name, role, avatar — and the whole row is the menu's trigger:
//
//   Display language   English / Deutsch (a web-tier preference; it used to be
//                      a select in the topbar, where it cost room on every
//                      screen for something chosen once)
//   Legal              Impressum, Datenschutz, Nutzungsbedingungen — the same
//                      three documents the footer links on every screen (#518),
//                      opened in a new tab so a running session's screen stays
//   Log out            AuthService.Logout deletes the server-side session row
//                      and clears the cookies; on success the cached queries
//                      are dropped and the app goes to /login
//
// Logging out does not stop a Voice Session — it only takes the console away
// from the GM. So while one is live, Log out asks first and says exactly that.

// The server's role value → what the GM reads. Today every web user is the
// 'operator' of their table, which in the product's own words is its GM; a
// value this map does not know is shown as the server sent it rather than
// hidden.
const ROLE_LABELS: Record<string, MessageKey> = {
  operator: "shell.roleGm",
  gm: "shell.roleGm",
};

function roleLabel(role: string, t: TFunc): string {
  const key = ROLE_LABELS[role];
  return key ? t(key) : role;
}

// The legal documents' German proper nouns stay verbatim in every display
// language, as in the footer (#518).
const LEGAL_LINKS = [
  { href: "/imprint", label: "Impressum" },
  { href: "/privacy", label: "Datenschutz" },
  { href: "/terms", label: "Nutzungsbedingungen" },
] as const;

export function SidebarUser({ user }: { user: User }) {
  const { t, lang, setLang } = useI18n();
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const { live } = useLiveSession();
  const [confirming, setConfirming] = useState(false);

  const logout = useMutation(AuthService.method.logout, {
    onSuccess: () => {
      queryClient.clear();
      void navigate({ to: "/login" });
    },
    // A failed logout must say so (ADR-0017: sonner): a menu that just closes
    // reads as "nothing happened" at best and "I'm logged out" at worst — on a
    // shared machine the session is still live.
    onError: (err) => toast.error(t("components.logOutFailed", { message: errorMessage(err) })),
  });

  const askLogout = () => {
    if (live) setConfirming(true);
    else logout.mutate({});
  };

  // While acting as a Campaign Guest the user is not this table's GM, whatever
  // role their own table gives them.
  const { guest } = useCan();
  const role = guest ? t("access.roleGuest") : roleLabel(user.role, t);

  return (
    <div className="gx-sidebar__user">
      <Menu
        side="top"
        align="start"
        className="gx-account-menu"
        trigger={
          <button
            type="button"
            className="gx-sidebar__account"
            // Named for what it opens AND whose it is; the visible name and
            // role inside are the same information for a sighted user.
            aria-label={t("shell.accountMenu", { name: user.name })}
          >
            {/* No presence dot: the green "live" dot here was hardcoded and
                read as "a session is live". The real live signal is on the
                Session nav item and in the topbar (useLiveSession). */}
            <Avatar name={user.name} src={user.avatar || null} size="sm" />
            <span className="gx-sidebar__user-meta">
              <span className="gx-sidebar__user-name">{user.name}</span>
              {role && <span className="gx-sidebar__user-role">{role}</span>}
            </span>
            <ChevronsUpDown size={14} className="gx-sidebar__account-chevron" aria-hidden="true" />
          </button>
        }
      >
        <MenuLabel>{t("common.displayLanguage")}</MenuLabel>
        {/* A radio group, so assistive tech hears "English, checked, 1 of 2"
            rather than two unrelated actions. Each language is named in
            ITSELF: a GM stuck in a language they cannot read can still find
            their own. */}
        <RMenu.RadioGroup value={lang} onValueChange={(next) => setLang(next as Lang)}>
          {LANGS.map((l) => (
            <RMenu.RadioItem key={l} value={l} className="gx-menu__item" lang={l}>
              <span className="gx-menu__icon" aria-hidden="true">
                <RMenu.ItemIndicator>
                  <Check />
                </RMenu.ItemIndicator>
              </span>
              <span className="gx-menu__text">{LANG_NAMES[l]}</span>
            </RMenu.RadioItem>
          ))}
        </RMenu.RadioGroup>

        <MenuSeparator />
        <MenuLabel>{t("components.legalNavLabel")}</MenuLabel>
        {LEGAL_LINKS.map((link) => (
          // Real anchors (asChild): middle-click and "copy link" work, and the
          // new tab keeps whatever the GM was doing — a live session's screen
          // above all — where it is.
          <RMenu.Item key={link.href} asChild>
            <a className="gx-menu__item" href={link.href} target="_blank" rel="noreferrer" lang="de">
              <span className="gx-menu__icon" aria-hidden="true" />
              <span className="gx-menu__text">{link.label}</span>
              <span className="gx-menu__hint">
                <ExternalLink size={13} aria-hidden="true" />
                <span className="gx-visually-hidden">{t("shell.opensInNewTab")}</span>
              </span>
            </a>
          </RMenu.Item>
        ))}

        <MenuSeparator />
        <MenuItem icon={<LogOut />} onSelect={askLogout} disabled={logout.isPending}>
          {t("components.logOut")}
        </MenuItem>
      </Menu>

      <ConfirmDialog
        open={confirming}
        onOpenChange={setConfirming}
        title={t("shell.logOutLiveTitle")}
        description={t("shell.logOutLiveDesc")}
        confirmLabel={t("components.logOut")}
        destructive={false}
        onConfirm={() => {
          setConfirming(false);
          logout.mutate({});
        }}
      />
    </div>
  );
}
