package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/bean-la/wrklogr/internal/config"
	stripeclient "github.com/bean-la/wrklogr/internal/stripe"
	"github.com/spf13/cobra"
)

func newStripeInvoiceStatusCmd(getConfig func() (*config.RuntimeConfig, error)) *cobra.Command {
	var invoiceID, stripeKey string

	cmd := &cobra.Command{
		Use:   "stripe-invoice-status",
		Short: "Read a Stripe invoice's payment state and hosted URL",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := getConfig()
			if err != nil {
				return err
			}
			invoiceID = strings.TrimSpace(invoiceID)
			if invoiceID == "" {
				return fmt.Errorf("--invoice-id is required")
			}
			key := resolveStripeAPIKey(cfg, stripeKey)
			if key == "" {
				return fmt.Errorf("Stripe secret key required via --stripe-key, STRIPE_SECRET_KEY, or config")
			}
			invoice, err := stripeclient.NewClient(key, nil).GetInvoice(context.Background(), invoiceID)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "invoice: %s\nstatus: %s\npaid: %t\namount due: $%.2f\namount paid: $%.2f\n", invoice.ID, invoice.Status, invoice.Paid, float64(invoice.AmountDue)/100, float64(invoice.AmountPaid)/100)
			if invoice.HostedInvoiceURL != "" {
				fmt.Fprintf(out, "hosted URL: %s\n", invoice.HostedInvoiceURL)
			}
			if invoice.StatusTransitions.PaidAt > 0 {
				fmt.Fprintf(out, "paid at: %s\n", time.Unix(invoice.StatusTransitions.PaidAt, 0).UTC().Format(time.RFC3339))
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&invoiceID, "invoice-id", "", "Stripe invoice ID")
	cmd.Flags().StringVar(&stripeKey, "stripe-key", "", "Stripe secret API key")
	return cmd
}
