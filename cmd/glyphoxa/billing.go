package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"text/tabwriter"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/MrWong99/Glyphoxa/internal/billing"
	"github.com/MrWong99/Glyphoxa/internal/storage"
)

// billingUsage documents the `glyphoxa billing` subcommand (ADR-0054): the
// operator surface for the Plan catalog, Tenant subscriptions, and the cost +
// revenue report. There is deliberately NO payment-processor integration here —
// the operator collects money out of band and records the resulting plan
// binding; automated billing is a later layer.
const billingUsage = `usage: glyphoxa billing <plans-sync|plans-list|tenants|subscribe|cancel|report>

  plans-sync -file <plans.json> [-archive-missing]
           sync the declarative plan catalog into the DB (upsert by slug;
           -archive-missing archives plans absent from the file)
  plans-list
           list the plan catalog (archived plans flagged)
  tenants  list tenants with their active plan
  subscribe -tenant <uuid> -plan <slug>
           bind a tenant to a plan (snapshots the current price; ends any
           previous subscription)
  cancel -tenant <uuid>
           end a tenant's active subscription
  report [-month YYYY-MM]
           per-tenant revenue (subscription price snapshots, un-prorated) and
           estimated provider cost (usage ledger) for one calendar month (UTC);
           defaults to the current month

Connection string is read from $GLYPHOXA_DATABASE_URL (or $DATABASE_URL).
Every cost figure is an ESTIMATE from the static price map (ADR-0046), never
billing truth.`

// RunBilling is the entry point for the `billing` subcommand. args are the
// arguments after "billing".
func RunBilling(ctx context.Context, args []string) error {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, billingUsage)
		return fmt.Errorf("billing: missing subcommand")
	}
	switch args[0] {
	case "-h", "--help", "help":
		fmt.Println(billingUsage)
		return nil
	}

	dsn := databaseURL()
	if dsn == "" {
		return fmt.Errorf("billing: set $GLYPHOXA_DATABASE_URL (or $DATABASE_URL)")
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return fmt.Errorf("billing: open db: %w", err)
	}
	defer pool.Close()
	st := storage.New(pool)

	switch args[0] {
	case "plans-sync":
		return runPlansSync(ctx, st, args[1:])
	case "plans-list":
		return runPlansList(ctx, st)
	case "tenants":
		return runBillingTenants(ctx, st)
	case "subscribe":
		return runSubscribe(ctx, st, args[1:])
	case "cancel":
		return runCancel(ctx, st, args[1:])
	case "report":
		return runBillingReport(ctx, st, args[1:])
	default:
		fmt.Fprintln(os.Stderr, billingUsage)
		return fmt.Errorf("billing: unknown subcommand %q", args[0])
	}
}

func runPlansSync(ctx context.Context, st *storage.Store, args []string) error {
	fs := flag.NewFlagSet("billing plans-sync", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	file := fs.String("file", "", "path to the plan catalog JSON file")
	archiveMissing := fs.Bool("archive-missing", false, "archive plans absent from the file (never deletes)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *file == "" {
		return fmt.Errorf("billing plans-sync: -file is required")
	}

	data, err := os.ReadFile(*file)
	if err != nil {
		return fmt.Errorf("billing plans-sync: %w", err)
	}
	catalog, err := billing.ParseCatalog(data)
	if err != nil {
		return err
	}

	specs := make([]storage.PlanSpec, 0, len(catalog.Plans))
	for _, p := range catalog.Plans {
		limits, err := p.LimitsJSON()
		if err != nil {
			return err
		}
		specs = append(specs, storage.PlanSpec{
			Slug:             p.Slug,
			DisplayName:      p.DisplayName,
			Description:      p.Description,
			MonthlyPriceUSD:  p.MonthlyPriceUSD,
			KeySource:        string(p.KeySource),
			IncludedUsageUSD: p.IncludedUsageUSD,
			Limits:           limits,
		})
	}

	res, err := st.SyncPlans(ctx, specs, *archiveMissing)
	if err != nil {
		return err
	}
	fmt.Printf("billing: plans synced — %d upserted, %d archived\n", res.Upserted, res.Archived)
	return nil
}

func runPlansList(ctx context.Context, st *storage.Store) error {
	plans, err := st.ListPlans(ctx)
	if err != nil {
		return err
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "SLUG\tNAME\tPRICE/MO\tKEYS\tINCLUDED USAGE\tSTATE")
	for _, p := range plans {
		included := "-"
		if p.IncludedUsageUSD != nil {
			included = fmt.Sprintf("$%.2f", *p.IncludedUsageUSD)
		}
		state := "active"
		if p.Archived {
			state = "archived"
		}
		fmt.Fprintf(w, "%s\t%s\t$%.2f\t%s\t%s\t%s\n",
			p.Slug, p.DisplayName, p.MonthlyPriceUSD, p.KeySource, included, state)
	}
	return w.Flush()
}

func runBillingTenants(ctx context.Context, st *storage.Store) error {
	tenants, err := st.ListTenantsWithPlan(ctx)
	if err != nil {
		return err
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "TENANT ID\tNAME\tPLAN\tCREATED")
	for _, t := range tenants {
		plan := t.PlanSlug
		if plan == "" {
			plan = "-"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", t.ID, t.Name, plan, t.CreatedAt.Format(time.DateOnly))
	}
	return w.Flush()
}

func runSubscribe(ctx context.Context, st *storage.Store, args []string) error {
	fs := flag.NewFlagSet("billing subscribe", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	tenant := fs.String("tenant", "", "tenant id (see `glyphoxa billing tenants`)")
	plan := fs.String("plan", "", "plan slug (see `glyphoxa billing plans-list`)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *tenant == "" || *plan == "" {
		return fmt.Errorf("billing subscribe: -tenant and -plan are required")
	}
	tenantID, err := uuid.Parse(*tenant)
	if err != nil {
		return fmt.Errorf("billing subscribe: bad tenant id: %w", err)
	}

	sub, err := st.SetTenantPlan(ctx, tenantID, *plan)
	if err != nil {
		return err
	}
	fmt.Printf("billing: tenant %s subscribed to %q at $%.2f/mo (snapshot)\n",
		sub.TenantID, sub.PlanSlug, sub.MonthlyPriceUSD)
	return nil
}

func runCancel(ctx context.Context, st *storage.Store, args []string) error {
	fs := flag.NewFlagSet("billing cancel", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	tenant := fs.String("tenant", "", "tenant id")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *tenant == "" {
		return fmt.Errorf("billing cancel: -tenant is required")
	}
	tenantID, err := uuid.Parse(*tenant)
	if err != nil {
		return fmt.Errorf("billing cancel: bad tenant id: %w", err)
	}
	if err := st.EndTenantPlan(ctx, tenantID); err != nil {
		return err
	}
	fmt.Printf("billing: tenant %s subscription ended\n", tenantID)
	return nil
}

func runBillingReport(ctx context.Context, st *storage.Store, args []string) error {
	fs := flag.NewFlagSet("billing report", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	month := fs.String("month", "", "calendar month YYYY-MM (UTC); defaults to the current month")
	if err := fs.Parse(args); err != nil {
		return err
	}

	var from time.Time
	if *month == "" {
		now := time.Now().UTC()
		from = time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	} else {
		var err error
		from, err = time.Parse("2006-01", *month)
		if err != nil {
			return fmt.Errorf("billing report: bad -month (want YYYY-MM): %w", err)
		}
	}
	to := from.AddDate(0, 1, 0)

	lines, err := st.BillingReport(ctx, from, to)
	if err != nil {
		return err
	}

	fmt.Printf("Billing report %s (UTC month). Revenue = subscription price snapshots,\n", from.Format("2006-01"))
	fmt.Println("un-prorated; cost = usage-ledger ESTIMATES (ADR-0046), never billing truth.")
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "TENANT\tPLAN\tREVENUE/MO\tEST. COST\tLLM TOK (IN/OUT)\tTTS CHARS\tSTT SEC")
	var revenue, cost float64
	for _, l := range lines {
		plan := l.PlanSlug
		if plan == "" {
			plan = "- (BYOK, unsubscribed)"
		}
		fmt.Fprintf(w, "%s\t%s\t$%.2f\t$%.4f\t%d/%d\t%d\t%.0f\n",
			l.TenantName, plan, l.MonthlyPriceUSD, l.EstimatedUSD,
			l.LLMInputTokens, l.LLMOutputTokens, l.TTSCharacters, l.STTAudioSeconds)
		revenue += l.MonthlyPriceUSD
		cost += l.EstimatedUSD
	}
	if err := w.Flush(); err != nil {
		return err
	}
	fmt.Printf("\nTOTAL revenue $%.2f, estimated provider cost $%.4f, margin $%.4f\n",
		revenue, cost, revenue-cost)
	return nil
}
