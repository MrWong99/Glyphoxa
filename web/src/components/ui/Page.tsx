import type { HTMLAttributes, ReactNode } from "react";

// Page + PageHeader — the screen container and its header, so every screen
// sits in the same centred column with the same gutter (--page-gutter: 32px,
// 24px at 880 and below, 16px on a phone) and opens with the same header.
//
//   <Page width="narrow">
//     <PageHeader
//       eyebrow="Campaign"
//       title="Settings"
//       description="Language, system and the voice the Butler answers with."
//       actions={<Button onClick={save}>Save changes</Button>}
//     />
//     …
//   </Page>
//
// width="wide" (default, 1152px) is for lists with a detail pane; "narrow"
// (820px) for forms and settings, where a full-width line is too long to read.

export function Page({
  width = "wide",
  className = "",
  children,
  ...props
}: {
  width?: "narrow" | "wide";
} & HTMLAttributes<HTMLDivElement>) {
  const cls = ["gx-page", width === "narrow" ? "gx-page--narrow" : "", className].filter(Boolean).join(" ");
  return (
    <div className={cls} {...props}>
      {children}
    </div>
  );
}

// PageHeader — eyebrow (the section the page belongs to), the page's one h1, a
// sentence of description, and the page-level actions at the trailing edge
// (they wrap under the title on a narrow screen). The title is the h1 by
// default; pass titleAs="h2" when the page is a view inside a screen that
// already has its h1.
export function PageHeader({
  eyebrow,
  title,
  description,
  actions,
  titleAs: Title = "h1",
  className = "",
}: {
  eyebrow?: ReactNode;
  title: ReactNode;
  description?: ReactNode;
  /** Buttons for the page as a whole. */
  actions?: ReactNode;
  titleAs?: "h1" | "h2";
  className?: string;
}) {
  return (
    <header className={className ? `gx-page-header ${className}` : "gx-page-header"}>
      <div className="gx-page-header__text">
        {eyebrow && <div className="gx-overline">{eyebrow}</div>}
        <Title className="gx-page-header__title">{title}</Title>
        {description && <p className="gx-page-header__desc">{description}</p>}
      </div>
      {actions && <div className="gx-page-header__actions">{actions}</div>}
    </header>
  );
}
