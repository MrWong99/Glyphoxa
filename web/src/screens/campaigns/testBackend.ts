import { Code, ConnectError, createRouterTransport } from "@connectrpc/connect";
import { create } from "@bufbuild/protobuf";
import { timestampFromDate } from "@bufbuild/protobuf/wkt";

import {
  ArchiveCampaignResponseSchema,
  CampaignSchema,
  CampaignService,
  CreateCampaignResponseSchema,
  DeleteCampaignResponseSchema,
  GetActiveCampaignResponseSchema,
  GetCampaignRosterResponseSchema,
  GetSessionResponseSchema,
  ListCampaignsResponseSchema,
  ListSupportedLanguagesResponseSchema,
  SessionService,
  SetActiveCampaignResponseSchema,
  UnarchiveCampaignResponseSchema,
  UpdateCampaignResponseSchema,
  type UpdateCampaignRequest,
} from "@gen/glyphoxa/management/v1/management_pb";

// A stateful in-memory CampaignService for the campaign-management tests (the
// Campaigns page, the Settings view, the quick switcher). It follows the
// server's rules where the UI depends on them:
//
//   - resolution is live-first, then the durable selection, then the most
//     recently created campaign that is not archived (internal/rpc
//     resolveActiveCampaign); none left is CodeNotFound;
//   - archiving clears a selection that pointed at the campaign;
//   - the live Voice Session's campaign can be neither archived nor deleted
//     (CodeFailedPrecondition), and only an archived campaign can be deleted.

export type Camp = {
  id: string;
  name: string;
  system?: string;
  language?: string;
  archived?: boolean;
  /** ISO dates; default to a fixed day so a rendered date is predictable. */
  created?: string;
  updated?: string;
  tapeArmed?: boolean;
  highlightBar?: number;
  highlightConfirmWindows?: number;
};

type Row = Required<Omit<Camp, "archived">> & { archivedAt?: string };

export type Call =
  | { method: "listCampaigns"; includeArchived: boolean }
  | { method: "setActiveCampaign"; id: string }
  | { method: "createCampaign"; name: string }
  | { method: "updateCampaign"; req: UpdateCampaignRequest }
  | { method: "archiveCampaign"; id: string }
  | { method: "unarchiveCampaign"; id: string }
  | { method: "deleteCampaign"; id: string };

export function campaignBackend(
  opts: {
    campaigns?: Camp[];
    /** The durable selection. */
    activeId?: string;
    /** A Voice Session is live in this campaign. */
    liveOn?: string;
    /** Fail this many ListCampaigns calls before answering. */
    failLists?: number;
    /** Fail this many GetActiveCampaign calls (Unavailable) before answering. */
    failActive?: number;
    /** Refuse these RPCs with the given message (CodeInternal / InvalidArgument). */
    errors?: Partial<Record<"createCampaign" | "setActiveCampaign" | "updateCampaign" | "deleteCampaign", string>>;
  } = {},
) {
  const state = {
    campaigns: (opts.campaigns ?? []).map(
      (c): Row => ({
        id: c.id,
        name: c.name,
        system: c.system ?? "",
        language: c.language ?? "",
        created: c.created ?? "2026-03-01T12:00:00Z",
        updated: c.updated ?? c.created ?? "2026-03-01T12:00:00Z",
        tapeArmed: c.tapeArmed ?? false,
        highlightBar: c.highlightBar ?? 0,
        highlightConfirmWindows: c.highlightConfirmWindows ?? 0,
        archivedAt: c.archived ? "2026-07-09T12:00:00Z" : undefined,
      }),
    ),
    selected: opts.activeId as string | undefined,
    liveOn: opts.liveOn as string | undefined,
    failLists: opts.failLists ?? 0,
    failActive: opts.failActive ?? 0,
    calls: [] as Call[],
    next: 1,
  };

  const proto = (c: Row) =>
    create(CampaignSchema, {
      id: c.id,
      name: c.name,
      system: c.system,
      language: c.language,
      createdAt: timestampFromDate(new Date(c.created)),
      updatedAt: timestampFromDate(new Date(c.updated)),
      archivedAt: c.archivedAt ? timestampFromDate(new Date(c.archivedAt)) : undefined,
      tapeArmed: c.tapeArmed,
      highlightBar: c.highlightBar,
      highlightConfirmWindows: c.highlightConfirmWindows,
    });

  const find = (id: string) => {
    const c = state.campaigns.find((x) => x.id === id);
    if (!c) throw new ConnectError("campaign not found", Code.NotFound);
    return c;
  };

  const resolved = (): Row | undefined => {
    if (state.liveOn) return state.campaigns.find((c) => c.id === state.liveOn);
    const inUse = state.campaigns.filter((c) => !c.archivedAt);
    return (
      inUse.find((c) => c.id === state.selected) ??
      [...inUse].sort((a, b) => b.created.localeCompare(a.created) || b.id.localeCompare(a.id))[0]
    );
  };
  const requireResolved = () => {
    const c = resolved();
    if (!c) throw new ConnectError("no active campaign", Code.NotFound);
    return c;
  };

  const liveGuard = (id: string, verb: string) => {
    if (state.liveOn === id) {
      throw new ConnectError(
        `campaign backs the live Voice Session and cannot be ${verb} while it runs`,
        Code.FailedPrecondition,
      );
    }
  };

  const transport = createRouterTransport(({ service }) => {
    service(CampaignService, {
      listCampaigns: (req) => {
        state.calls.push({ method: "listCampaigns", includeArchived: req.includeArchived });
        if (state.failLists > 0) {
          state.failLists--;
          throw new ConnectError("campaign store offline", Code.Unavailable);
        }
        return create(ListCampaignsResponseSchema, {
          campaigns: state.campaigns.filter((c) => req.includeArchived || !c.archivedAt).map(proto),
        });
      },
      getActiveCampaign: () => {
        if (state.failActive > 0) {
          state.failActive--;
          throw new ConnectError("campaign store offline", Code.Unavailable);
        }
        return create(GetActiveCampaignResponseSchema, { campaign: proto(requireResolved()) });
      },
      getCampaignRoster: () =>
        create(GetCampaignRosterResponseSchema, { campaign: proto(requireResolved()), roster: [] }),
      setActiveCampaign: (req) => {
        state.calls.push({ method: "setActiveCampaign", id: req.campaignId });
        if (opts.errors?.setActiveCampaign) throw new ConnectError(opts.errors.setActiveCampaign, Code.Internal);
        const c = find(req.campaignId);
        if (c.archivedAt) throw new ConnectError("campaign is archived", Code.FailedPrecondition);
        state.selected = c.id;
        return create(SetActiveCampaignResponseSchema, { campaign: proto(requireResolved()) });
      },
      createCampaign: (req) => {
        state.calls.push({ method: "createCampaign", name: req.name });
        if (opts.errors?.createCampaign) throw new ConnectError(opts.errors.createCampaign, Code.Internal);
        const c: Row = {
          id: `new-${state.next++}`,
          name: req.name,
          system: req.system,
          language: req.language,
          created: "2026-11-22T12:00:00Z",
          updated: "2026-11-22T12:00:00Z",
          tapeArmed: false,
          highlightBar: 0,
          highlightConfirmWindows: 0,
        };
        state.campaigns.push(c);
        return create(CreateCampaignResponseSchema, { campaign: proto(c) });
      },
      updateCampaign: (req) => {
        if (opts.errors?.updateCampaign) throw new ConnectError(opts.errors.updateCampaign, Code.InvalidArgument);
        state.calls.push({ method: "updateCampaign", req });
        const c = find(req.id);
        c.name = req.name;
        c.system = req.system;
        c.language = req.language;
        if (req.tapeArmed !== undefined) c.tapeArmed = req.tapeArmed;
        if (req.highlightBar !== undefined) c.highlightBar = req.highlightBar;
        if (req.highlightConfirmWindows !== undefined) c.highlightConfirmWindows = req.highlightConfirmWindows;
        c.updated = "2026-11-22T12:00:00Z";
        return create(UpdateCampaignResponseSchema, { campaign: proto(c) });
      },
      archiveCampaign: (req) => {
        state.calls.push({ method: "archiveCampaign", id: req.id });
        const c = find(req.id);
        liveGuard(c.id, "archived");
        c.archivedAt = "2026-11-22T12:00:00Z";
        if (state.selected === c.id) state.selected = undefined;
        return create(ArchiveCampaignResponseSchema, { campaign: proto(c) });
      },
      unarchiveCampaign: (req) => {
        state.calls.push({ method: "unarchiveCampaign", id: req.id });
        const c = find(req.id);
        c.archivedAt = undefined;
        return create(UnarchiveCampaignResponseSchema, { campaign: proto(c) });
      },
      deleteCampaign: (req) => {
        state.calls.push({ method: "deleteCampaign", id: req.id });
        const c = find(req.id);
        liveGuard(c.id, "deleted");
        if (opts.errors?.deleteCampaign) throw new ConnectError(opts.errors.deleteCampaign, Code.Internal);
        if (!c.archivedAt) {
          throw new ConnectError("campaign must be archived before deletion", Code.FailedPrecondition);
        }
        state.campaigns = state.campaigns.filter((x) => x.id !== c.id);
        return create(DeleteCampaignResponseSchema, {});
      },
      listSupportedLanguages: () => create(ListSupportedLanguagesResponseSchema, { languages: ["de", "en"] }),
    });
    service(SessionService, {
      getSession: () => create(GetSessionResponseSchema, { active: state.liveOn !== undefined }),
    });
  });

  /** The calls of one kind, in order. */
  const callsOf = <M extends Call["method"]>(method: M) =>
    state.calls.filter((c): c is Extract<Call, { method: M }> => c.method === method);

  return { transport, state, callsOf };
}
