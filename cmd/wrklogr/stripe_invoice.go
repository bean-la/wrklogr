package main

import (
	"context"
	"fmt"
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
		description := strings.TrimSpace(entry.Description)
		if description != "" && !containsString(day.Descriptions, description) {
			day.Descriptions = append(day.Descriptions, description)
		}
	}
	days := make([]stripeInvoiceDay, 0, len(byDate))
	for _, day := range byDate {
		day.AmountCents = (int64(day.Minutes)*ratesCents + 240) / 480
		days = append(days, *day)
	}
	sort.Slice(days, func(i, j int) bool { return days[i].Date < days[j].Date })
	return days, nil
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
	var customerID, projectIDInput, sinceInput, untilInput, dayRateInput, description string
	var stripeKey, nokoToken string
	var daysUntilDue int
	var create bool

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
			entries, err := noko.NewClient(token, nil).ListEntries(context.Background(), sinceInput, untilInput, nil, []int{projectID})
			if err != nil {
				return err
			}
			days, err := aggregateStripeInvoiceDays(entries, rate)
			if err != nil {
				return err
			}
			if len(days) == 0 {
				return fmt.Errorf("no billable Noko entries found")
			}
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "customer: %s\nperiod: %s through %s\nproject: %d\n", customerID, sinceInput, untilInput, projectID)
			var total int64
			for _, day := range days {
				total += day.AmountCents
				fmt.Fprintf(out, "  %s  %.2fh  $%.2f  %s\n", day.Date, float64(day.Minutes)/60, float64(day.AmountCents)/100, stripeDayDescription(day, rate))
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
			invoice, err := stripe.CreateInvoice(context.Background(), stripeclient.CreateInvoiceParams{
				Customer: customerID, Description: description, DaysUntilDue: daysUntilDue, CollectionMethod: "send_invoice",
			})
			if err != nil {
				return err
			}
			for _, day := range days {
				if err := stripe.CreateInvoiceItem(context.Background(), stripeclient.CreateInvoiceItemParams{
					Customer: customerID, Invoice: invoice.ID, Amount: day.AmountCents, Currency: "usd", Description: stripeDayDescription(day, rate),
				}); err != nil {
					return fmt.Errorf("invoice %s created, but line item for %s failed: %w", invoice.ID, day.Date, err)
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
	cmd.Flags().StringVar(&description, "description", "", "Stripe invoice description")
	cmd.Flags().StringVar(&nokoToken, "noko-token", "", "Noko API token")
	cmd.Flags().StringVar(&stripeKey, "stripe-key", "", "Stripe secret API key")
	cmd.Flags().IntVar(&daysUntilDue, "days-until-due", 30, "Days until payment is due")
	cmd.Flags().BoolVar(&create, "create", false, "Create a Stripe draft invoice after previewing")
	return cmd
}
