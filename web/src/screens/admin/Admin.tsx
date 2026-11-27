import { useQuery } from "@connectrpc/connect-query";

import {
  AdminService,
  AdmissionMode,
  type GetAccessOverviewResponse,
} from "@gen/glyphoxa/management/v1/management_pb";
import { Alert } from "@/components/ui/Alert";
import { Page, PageHeader } from "@/components/ui/Page";
import { QueryBoundary } from "@/components/ui/QueryBoundary";
import { Skeleton } from "@/components/ui/Skeleton";
import { useI18n } from "@/i18n";
import { AddPersonCard } from "./AddPersonCard";
import { InvitationLinks } from "./InvitationLinks";
import { PeopleSection } from "./PeopleSection";

import "@/components/access/access.css";

// The Admin screen (ADR-0065): who may use this instance, and whose tables may
// spend its Platform Keys. It exists for a Platform Admin only — the route
// (app/router.tsx) renders the not-found page for anyone else and never mounts
// this component, and every AdminService procedure re-checks on the server.
//
// One read feeds the whole screen, AdminService.GetAccessOverview: the
// deployment's Admission Mode, the people, and the access invitation links.
// Every write here ends by re-reading it (the Setup screen's refetch-on-success
// rule), so the list on screen is always the server's — a row never shows a
// status the server did not confirm.
//
// What it deliberately does not do: open another table's Campaigns. A Platform
// Admin can cut a table off from here but cannot read it (ADR-0065, "no
// impersonation").
export function Admin() {
  const { t } = useI18n();
  const overview = useQuery(AdminService.method.getAccessOverview, {});

  return (
    <Page className="gx-admin">
      <PageHeader title={t("shell.titleAdmin")} description={t("access.adminLede")} />
      <QueryBoundary
        query={overview}
        skeleton={<Skeleton.Row count={5} label={t("access.loading")} />}
        errorTitle={t("access.loadErrorTitle")}
      >
        {(data) => <AccessOverview data={data} />}
      </QueryBoundary>
    </Page>
  );
}

function AccessOverview({ data }: { data: GetAccessOverviewResponse }) {
  const { t } = useI18n();
  // Only an explicit OPEN answer is treated as open sign-up. An unspecified
  // mode (a server that predates the field) reads as invite-only, the default
  // and the stricter of the two — the same fail-safe the login screen takes.
  const open = data.admissionMode === AdmissionMode.OPEN;

  return (
    <>
      {/* Part of the page, not an event: read in place (role=note), and in open
          mode it is the sentence that keeps the rest of the screen honest —
          grants and links are recorded there but do not gate sign-in. */}
      <Alert tone="info" role="note" className="gx-admin__mode">
        {open ? t("access.modeOpen") : t("access.modeInviteOnly")}
      </Alert>

      <PeopleSection people={data.people} open={open} />
      <AddPersonCard open={open} people={data.people} />
      <InvitationLinks invitations={data.invitations} open={open} />
    </>
  );
}
