package rpc

import (
	"context"
	"errors"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	managementv1 "github.com/MrWong99/Glyphoxa/gen/glyphoxa/management/v1"
	"github.com/MrWong99/Glyphoxa/internal/storage"
)

// GetUsageSummary reports the Tenant's plan and this month's estimated usage
// against its included allowance, so Setup shows the allowance before a
// feature refuses (ADR-0054/0055). The figures come from the reads the gates
// themselves use: the plan row of the active subscription, and the allowance
// state (spend.PlanAllowance) over the UTC calendar month. With no allowance
// reader wired — allowlist mode, where no allowance gates anything — no
// included amount is reported, whatever the plan row says, because none is
// enforced. A read (NO_SIDE_EFFECTS).
func (s *ProviderServer) GetUsageSummary(
	ctx context.Context,
	_ *connect.Request[managementv1.GetUsageSummaryRequest],
) (*connect.Response[managementv1.GetUsageSummaryResponse], error) {
	tenantID, err := s.tenant(ctx)
	if err != nil {
		return nil, err
	}

	resp := &managementv1.GetUsageSummaryResponse{
		ResetsAt: timestamppb.New(nextMonthUTC(s.now())),
		// The same entitlement every key resolution consults (W9), so the card
		// and the "Included" badges can never disagree.
		UsesPlatformKeys: s.platformKeyGate(ctx, tenantID)(storage.ComponentLLM),
	}

	if s.plans != nil {
		plan, err := s.plans.TenantActivePlan(ctx, tenantID)
		switch {
		case err == nil:
			resp.PlanSlug, resp.PlanName = plan.Slug, plan.DisplayName
		case !errors.Is(err, storage.ErrNotFound):
			return nil, internalErr(s.log, "GetUsageSummary: active plan read failed", err)
		}
	}

	if s.allowance != nil {
		state, err := s.allowance.AllowanceState(ctx, tenantID)
		if err != nil {
			return nil, internalErr(s.log, "GetUsageSummary: allowance read failed", err)
		}
		if state.IncludedUSD != nil {
			included := *state.IncludedUSD
			resp.IncludedUsd = &included
			resp.MonthUsd = state.MonthUSD
			resp.Exhausted = state.Exhausted()
		}
	}
	return connect.NewResponse(resp), nil
}

// nextMonthUTC is the first instant of the UTC calendar month after t: when
// the allowance window of spend.PlanAllowance starts over.
func nextMonthUTC(t time.Time) time.Time {
	t = t.UTC()
	return time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.UTC).AddDate(0, 1, 0)
}
