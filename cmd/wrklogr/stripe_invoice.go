package main

import (
	"context"
	"crypto/sha256"
	"fmt"
	"math"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/bean-la/wrklogr/internal/config"
	"github.com/bean-la/wrklogr/internal/noko"
	stripeclient "github.com/bean-la/wrklogr/internal/stripe"
	"github.com/spf13/cobra"
)

type stripeInvoiceDay struct {
	Date         string
	Minutes      int
	AmountCents  int64
	Descriptions []string
	EntryIDs     []int
	Category     string
	RoundedDays  float64
	Rate         float64
}

type stripeInvoiceAdjustment struct {
	AmountCents int64
	Description string
	Metadata    map[string]string
}

func parseLegacyGratis(values []string, rates map[string]float64) ([]stripeInvoiceAdjustment, error) {
	var adjustments []stripeInvoiceAdjustment
	for _, value := range values {
		parts := strings.SplitN(value, "=", 2)
		if len(parts) != 2 {
			return nil, fmt.Errorf("invalid --gratis-days %q (use category=days)", value)
		}
		category := strings.ToLower(strings.TrimSpace(parts[0]))
		days, err := strconv.ParseFloat(strings.TrimSpace(parts[1]), 64)
		if err != nil || days < 0 {
			return nil, fmt.Errorf("invalid gratis days %q", value)
		}
		rate := rates[category]
		if rate <= 0 {
			return nil, fmt.Errorf("no rate configured for gratis category %q", category)
		}
		amount := int64(math.Round(days * rate * 100))
		if amount == 0 {
			continue
		}
		adjustments = append(adjustments, stripeInvoiceAdjustment{
			AmountCents: -amount,
			Description: fmt.Sprintf("Gratis — %s — %.2f days at $%.2f/day", category, days, rate),
			Metadata: map[string]string{
				"wrklogr_adjustment": "gratis",
				"legacy_category":    category,
				"legacy_days":        fmt.Sprintf("%.2f", days),
				"legacy_day_rate":    fmt.Sprintf("%.2f", rate),
			},
		})
	}
	return adjustments, nil
}

func buildLegacyAdjustments(days []stripeInvoiceDay, gratis []string, rates map[string]float64, totalOverride float64, discountNote string) ([]stripeInvoiceAdjustment, int64, error) {
	var subtotal int64
	for _, day := range days {
		subtotal += day.AmountCents
	}
	adjustments, err := parseLegacyGratis(gratis, rates)
	if err != nil {
		return nil, 0, err
	}
	for _, adjustment := range adjustments {
		subtotal += adjustment.AmountCents
	}
	if totalOverride > 0 {
		target := int64(math.Round(totalOverride * 100))
		if delta := target - subtotal; delta != 0 {
			description := "Legacy total override"
			if strings.TrimSpace(discountNote) != "" {
				description += " — " + strings.TrimSpace(discountNote)
			}
			adjustments = append(adjustments, stripeInvoiceAdjustment{
				AmountCents: delta,
				Description: description,
				Metadata: map[string]string{
					"wrklogr_adjustment": "total_override",
					"legacy_total":       fmt.Sprintf("%.2f", totalOverride),
				},
			})
			subtotal = target
		}
	} else {
		// BeanInvoice.vue displays Math.round(subTotal) when there is no
		// total_override. Preserve that final customer-facing behavior as a
		// cents adjustment on the Stripe invoice.
		target := int64(math.Round(float64(subtotal)/100)) * 100
		if delta := target - subtotal; delta != 0 {
			adjustments = append(adjustments, stripeInvoiceAdjustment{
				AmountCents: delta,
				Description: "Legacy whole-dollar total rounding",
				Metadata: map[string]string{
					"wrklogr_adjustment": "legacy_total_rounding",
				},
			})
			subtotal = target
		}
	}
	return adjustments, subtotal, nil
}

func aggregateStripeInvoiceDays(entries []noko.Entry, dayRate float64) ([]stripeInvoiceDay, error) {
	if dayRate <= 0 {
		return nil, fmt.Errorf("day rate must be greater than zero")
	}
	ratesCents := int64(dayRate*100 + 0.5)
	byDate := map[string]*stripeInvoiceDay{}
	for _, entry := range entries {
		date := strings.TrimSpace(entry.Date)
		if date == "" || entry.Minutes <= 0 {
			continue
		}
		day := byDate[date]
		if day == nil {
			day = &stripeInvoiceDay{Date: date}
			byDate[date] = day
		}
		day.Minutes += entry.Minutes
		if entry.ID > 0 {
			day.EntryIDs = append(day.EntryIDs, entry.ID)
		}
		description := strings.TrimSpace(entry.Description)
		if description != "" && !containsString(day.Descriptions, description) {
			day.Descriptions = append(day.Descriptions, description)
		}
	}
	days := make([]stripeInvoiceDay, 0, len(byDate))
	for _, day := range byDate {
		sort.Ints(day.EntryIDs)
		day.AmountCents = (int64(day.Minutes)*ratesCents + 240) / 480
		days = append(days, *day)
	}
	sort.Slice(days, func(i, j int) bool { return days[i].Date < days[j].Date })
	return days, nil
}

// aggregateLegacyQuarterDay collapses the period into one summary line using
// the Bean invoice API's historical rule: convert minutes to eight-hour days,
// round to the nearest quarter day, then multiply by the day rate.
func aggregateLegacyQuarterDay(entries []noko.Entry, dayRate float64) ([]stripeInvoiceDay, error) {
	return aggregateLegacyQuarterDayWithConfig(entries, dayRate, nil)
}

func aggregateLegacyQuarterDayWithConfig(entries []noko.Entry, dayRate float64, legacy *config.StripeLegacyConfig) ([]stripeInvoiceDay, error) {
	if dayRate <= 0 {
		return nil, fmt.Errorf("day rate must be greater than zero")
	}
	defaultCategory := "backend"
	if legacy != nil && strings.TrimSpace(legacy.DefaultCategory) != "" {
		defaultCategory = strings.ToLower(strings.TrimSpace(legacy.DefaultCategory))
	}
	minutesByCategory := map[string]int{}
	entryIDsByCategory := map[string][]int{}
	descriptionsByCategory := map[string][]string{}
	for _, entry := range entries {
		if entry.Minutes <= 0 {
			continue
		}
		category := classifyLegacyCategory(entry, legacy, defaultCategory)
		minutesByCategory[category] += entry.Minutes
		if entry.ID > 0 {
			entryIDsByCategory[category] = append(entryIDsByCategory[category], entry.ID)
		}
		if description := strings.TrimSpace(entry.Description); description != "" && !containsString(descriptionsByCategory[category], description) {
			descriptionsByCategory[category] = append(descriptionsByCategory[category], description)
		}
	}
	if len(minutesByCategory) == 0 {
		return nil, nil
	}
	order := []string{"backend", "frontend", "design", "pm", "qa", "other"}
	var lines []stripeInvoiceDay
	for _, category := range order {
		minutes := minutesByCategory[category]
		if minutes == 0 {
			continue
		}
		rate := dayRate
		if legacy != nil && legacy.Rates != nil && legacy.Rates[category] > 0 {
			rate = legacy.Rates[category]
		}
		if category == "other" && (legacy == nil || legacy.Rates[category] <= 0) {
			return nil, fmt.Errorf("legacy category %q has %d unpriced minutes; configure stripe.legacy.rates.%s or a default category", category, minutes, category)
		}
		entryIDs := entryIDsByCategory[category]
		sort.Ints(entryIDs)
		rawDays := float64(minutes) / 480.0
		roundedDays := math.Round(rawDays*4) / 4
		lines = append(lines, stripeInvoiceDay{
			Minutes: minutes, AmountCents: int64(math.Round(roundedDays * rate * 100)),
			Descriptions: descriptionsByCategory[category], EntryIDs: entryIDs,
			Category: category, RoundedDays: roundedDays, Rate: rate,
		})
	}
	return lines, nil
}

func classifyLegacyCategory(entry noko.Entry, legacy *config.StripeLegacyConfig, defaultCategory string) string {
	email := strings.ToLower(strings.TrimSpace(entry.User.Email))
	hasTag := func(wanted string) bool {
		for _, tag := range entry.Tags {
			if strings.EqualFold(strings.TrimSpace(tag.Name), wanted) {
				return true
			}
		}
		return false
	}
	hasUser := func(category string) bool {
		if legacy == nil || legacy.Users == nil {
			return false
		}
		for _, candidate := range legacy.Users[category] {
			if strings.EqualFold(strings.TrimSpace(candidate), email) {
				return true
			}
		}
		return false
	}
	if hasTag("development") {
		if hasUser("backend") {
			return "backend"
		}
		return "frontend"
	}
	if hasTag("QA") {
		return "qa"
	}
	if hasTag("design") {
		return "design"
	}
	if hasTag("PM") {
		return "pm"
	}
	for _, category := range []string{"backend", "frontend", "design"} {
		if hasUser(category) {
			return category
		}
	}
	return defaultCategory
}

func stripeInvoiceKey(days []stripeInvoiceDay, projectID int, since, until string, dayRate float64, rounding string) string {
	var ids []string
	for _, day := range days {
		for _, id := range day.EntryIDs {
			ids = append(ids, strconv.Itoa(id))
		}
	}
	sort.Strings(ids)
	input := fmt.Sprintf("noko:%d:%s:%s:%.2f:%s:%s", projectID, since, until, dayRate, rounding, strings.Join(ids, ","))
	return fmt.Sprintf("noko-%x", sha256.Sum256([]byte(input)))
}

func containsString(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func stripeDayDescription(day stripeInvoiceDay, dayRate float64) string {
	desc := strings.Join(day.Descriptions, "; ")
	if desc == "" {
		desc = "Noko work"
	}
	return fmt.Sprintf("%s — %s — %.2fh at $%.2f/day", day.Date, desc, float64(day.Minutes)/60, dayRate)
}

func legacyQuarterDayDescription(day stripeInvoiceDay, dayRate float64) string {
	rate := dayRate
	if day.Rate > 0 {
		rate = day.Rate
	}
	category := day.Category
	if category == "" {
		category = "work"
	}
	return fmt.Sprintf("Legacy Bean rounding — %s — %.2f days at $%.2f/day (%.2fh raw)", category, day.RoundedDays, rate, float64(day.Minutes)/60)
}

func resolveStripeAPIKey(cfg *config.RuntimeConfig, flag string) string {
	key := strings.TrimSpace(flag)
	if key == "" {
		key = strings.TrimSpace(os.Getenv("STRIPE_SECRET_KEY"))
	}
	if key == "" && cfg.Stripe != nil {
		key = strings.TrimSpace(cfg.Stripe.APIKey)
	}
	return key
}

func newStripeInvoiceCmd(getConfig func() (*config.RuntimeConfig, error)) *cobra.Command {
	var customerID, projectIDInput, sinceInput, untilInput, dayRateInput, description, rounding, discountNote string
	var stripeKey, nokoToken string
	var gratisDays []string
	var totalOverride float64
	var daysUntilDue int
	var create, includeInvoiced bool

	cmd := &cobra.Command{
		Use:   "stripe-invoice",
		Short: "Preview Noko work as Stripe invoice line items, or create a Stripe draft",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := getConfig()
			if err != nil {
				return err
			}
			customerID = strings.TrimSpace(customerID)
			if customerID == "" {
				return fmt.Errorf("--customer-id is required")
			}
			projectID := 0
			if cfg.Noko != nil {
				projectID = cfg.Noko.ProjectID
			}
			if projectIDInput != "" {
				projectID, err = strconv.Atoi(projectIDInput)
				if err != nil || projectID <= 0 {
					return fmt.Errorf("invalid --project-id %q", projectIDInput)
				}
			}
			if projectID <= 0 {
				return fmt.Errorf("--project-id is required (or configure noko.project_id)")
			}
			if sinceInput == "" || untilInput == "" {
				return fmt.Errorf("--since and --until are required")
			}
			if _, err := time.Parse("2006-01-02", sinceInput); err != nil {
				return fmt.Errorf("invalid --since: %w", err)
			}
			if _, err := time.Parse("2006-01-02", untilInput); err != nil {
				return fmt.Errorf("invalid --until: %w", err)
			}
			rate, err := strconv.ParseFloat(dayRateInput, 64)
			if err != nil || rate <= 0 {
				return fmt.Errorf("invalid --day-rate %q", dayRateInput)
			}
			token := strings.TrimSpace(nokoToken)
			if token == "" {
				token = strings.TrimSpace(os.Getenv("NOKO_TOKEN"))
			}
			if token == "" && cfg.Noko != nil {
				token = strings.TrimSpace(cfg.Noko.APIToken)
			}
			if token == "" {
				return fmt.Errorf("Noko token required via --noko-token, NOKO_TOKEN, or config")
			}
			nokoClient := noko.NewClient(token, nil)
			var entries []noko.Entry
			if includeInvoiced {
				if create {
					return fmt.Errorf("--include-invoiced is read-only and cannot be combined with --create")
				}
				allEntries, listErr := nokoClient.ListEntries(context.Background(), sinceInput, untilInput, nil, []int{projectID})
				if listErr != nil {
					return listErr
				}
				for _, entry := range allEntries {
					if entry.Billable {
						entries = append(entries, entry)
					}
				}
			} else {
				entries, err = nokoClient.ListInvoiceableEntries(context.Background(), sinceInput, untilInput, nil, []int{projectID})
			}
			if err != nil {
				return err
			}
			var days []stripeInvoiceDay
			switch rounding {
			case "daily-cents":
				days, err = aggregateStripeInvoiceDays(entries, rate)
			case "legacy-quarter-day":
				days, err = aggregateLegacyQuarterDayWithConfig(entries, rate, func() *config.StripeLegacyConfig {
					if cfg.Stripe != nil {
						return cfg.Stripe.Legacy
					}
					return nil
				}())
			default:
				return fmt.Errorf("invalid --rounding %q (use daily-cents or legacy-quarter-day)", rounding)
			}
			if err != nil {
				return err
			}
			if rounding != "legacy-quarter-day" && (len(gratisDays) > 0 || totalOverride > 0 || strings.TrimSpace(discountNote) != "") {
				return fmt.Errorf("--gratis-days, --total-override, and --discount-note require --rounding legacy-quarter-day")
			}
			if len(days) == 0 {
				return fmt.Errorf("no billable Noko entries found")
			}
			legacyRates := map[string]float64{"backend": rate, "frontend": rate, "design": rate, "pm": rate, "qa": rate, "other": rate}
			if cfg.Stripe != nil && cfg.Stripe.Legacy != nil && cfg.Stripe.Legacy.Rates != nil {
				for category, categoryRate := range cfg.Stripe.Legacy.Rates {
					if categoryRate > 0 {
						legacyRates[strings.ToLower(category)] = categoryRate
					}
				}
			}
			adjustments := []stripeInvoiceAdjustment(nil)
			adjustedTotal := int64(0)
			if rounding == "legacy-quarter-day" {
				adjustments, adjustedTotal, err = buildLegacyAdjustments(days, gratisDays, legacyRates, totalOverride, discountNote)
				if err != nil {
					return err
				}
			}
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "customer: %s\nperiod: %s through %s\nproject: %d\n", customerID, sinceInput, untilInput, projectID)
			var total int64
			for _, day := range days {
				total += day.AmountCents
				lineDescription := stripeDayDescription(day, rate)
				if rounding == "legacy-quarter-day" {
					lineDescription = legacyQuarterDayDescription(day, rate)
				}
				date := day.Date
				if date == "" {
					date = "period"
				}
				fmt.Fprintf(out, "  %s  %.2fh  $%.2f  %s\n", date, float64(day.Minutes)/60, float64(day.AmountCents)/100, lineDescription)
			}
			if len(adjustments) > 0 {
				for _, adjustment := range adjustments {
					total += adjustment.AmountCents
					fmt.Fprintf(out, "  adjustment  $%.2f  %s\n", float64(adjustment.AmountCents)/100, adjustment.Description)
				}
			}
			if rounding == "legacy-quarter-day" && (len(gratisDays) > 0 || totalOverride > 0) {
				total = adjustedTotal
			}
			fmt.Fprintf(out, "total: $%.2f\n", float64(total)/100)
			if !create {
				fmt.Fprintln(out, "dry run: no Stripe invoice created")
				return nil
			}
			key := resolveStripeAPIKey(cfg, stripeKey)
			if key == "" {
				return fmt.Errorf("Stripe secret key required via --stripe-key, STRIPE_SECRET_KEY, or config")
			}
			stripe := stripeclient.NewClient(key, nil)
			invoiceKey := stripeInvoiceKey(days, projectID, sinceInput, untilInput, rate, rounding)
			metadata := map[string]string{
				"wrklogr_source":      "noko",
				"wrklogr_invoice_key": invoiceKey,
				"noko_project_id":     strconv.Itoa(projectID),
				"noko_from":           sinceInput,
				"noko_until":          untilInput,
				"rounding":            rounding,
			}
			for _, status := range []string{"draft", "open"} {
				existing, listErr := stripe.ListInvoices(context.Background(), customerID, status)
				if listErr != nil {
					return listErr
				}
				for _, candidate := range existing {
					if candidate.Metadata["wrklogr_invoice_key"] == invoiceKey {
						fmt.Fprintf(out, "existing Stripe %s invoice: %s (no duplicate created)\n", candidate.Status, candidate.ID)
						return nil
					}
				}
			}
			invoice, err := stripe.CreateInvoice(context.Background(), stripeclient.CreateInvoiceParams{
				Customer: customerID, Description: description, DaysUntilDue: daysUntilDue, CollectionMethod: "send_invoice", Metadata: metadata,
			})
			if err != nil {
				return err
			}
			for _, day := range days {
				lineDescription := stripeDayDescription(day, rate)
				if rounding == "legacy-quarter-day" {
					lineDescription = legacyQuarterDayDescription(day, rate)
				}
				lineMetadata := map[string]string{
					"wrklogr_rounding": rounding,
					"noko_entry_ids":   joinEntryIDs(day.EntryIDs),
					"raw_minutes":      strconv.Itoa(day.Minutes),
				}
				if day.Category != "" {
					lineMetadata["legacy_category"] = day.Category
				}
				if day.RoundedDays > 0 {
					lineMetadata["legacy_rounded_days"] = fmt.Sprintf("%.2f", day.RoundedDays)
				}
				if day.Rate > 0 {
					lineMetadata["legacy_day_rate"] = fmt.Sprintf("%.2f", day.Rate)
				}
				if err := stripe.CreateInvoiceItem(context.Background(), stripeclient.CreateInvoiceItemParams{
					Customer: customerID, Invoice: invoice.ID, Amount: day.AmountCents, Currency: "usd", Description: lineDescription, Metadata: lineMetadata,
				}); err != nil {
					return fmt.Errorf("invoice %s created, but line item for %s failed: %w", invoice.ID, day.Date, err)
				}
			}
			for _, adjustment := range adjustments {
				if err := stripe.CreateInvoiceItem(context.Background(), stripeclient.CreateInvoiceItemParams{
					Customer: customerID, Invoice: invoice.ID, Amount: adjustment.AmountCents, Currency: "usd", Description: adjustment.Description, Metadata: adjustment.Metadata,
				}); err != nil {
					return fmt.Errorf("invoice %s created, but adjustment %q failed: %w", invoice.ID, adjustment.Description, err)
				}
			}
			fmt.Fprintf(out, "created Stripe draft: %s (not finalized or sent)\n", invoice.ID)
			return nil
		},
	}
	cmd.Flags().StringVar(&customerID, "customer-id", "", "Stripe customer ID")
	cmd.Flags().StringVar(&projectIDInput, "project-id", "", "Noko project ID (defaults to noko.project_id)")
	cmd.Flags().StringVar(&sinceInput, "since", "", "Start date (YYYY-MM-DD)")
	cmd.Flags().StringVar(&untilInput, "until", "", "End date (YYYY-MM-DD)")
	cmd.Flags().StringVar(&dayRateInput, "day-rate", "900", "Billing day rate in USD")
	cmd.Flags().StringVar(&rounding, "rounding", "daily-cents", "Rounding model: daily-cents or legacy-quarter-day")
	cmd.Flags().StringSliceVar(&gratisDays, "gratis-days", nil, "Legacy gratis deduction, repeatable as category=days (legacy-quarter-day only)")
	cmd.Flags().Float64Var(&totalOverride, "total-override", 0, "Legacy final total override in USD (legacy-quarter-day only)")
	cmd.Flags().StringVar(&discountNote, "discount-note", "", "Note attached to a legacy total override adjustment")
	cmd.Flags().StringVar(&description, "description", "", "Stripe invoice description")
	cmd.Flags().StringVar(&nokoToken, "noko-token", "", "Noko API token")
	cmd.Flags().StringVar(&stripeKey, "stripe-key", "", "Stripe secret API key")
	cmd.Flags().IntVar(&daysUntilDue, "days-until-due", 30, "Days until payment is due")
	cmd.Flags().BoolVar(&create, "create", false, "Create a Stripe draft invoice after previewing")
	cmd.Flags().BoolVar(&includeInvoiced, "include-invoiced", false, "Read-only audit mode: include historical Noko entries already marked invoiced")
	return cmd
}

func joinEntryIDs(ids []int) string {
	parts := make([]string, 0, len(ids))
	for _, id := range ids {
		parts = append(parts, strconv.Itoa(id))
	}
	return strings.Join(parts, ",")
}
