# Stripe invoicing

`wrklogr stripe-invoice` is the replacement path for creating a payable invoice from Noko work. Noko is the source of truth for time, while Stripe owns the customer, invoice, hosted payment page, and payment state. The command asks Noko for billable, not-yet-invoiced entries only, matching Noko's own invoice action.

The command is intentionally draft-first:

1. It reads Noko entries for a project and date range.
2. It groups entries by day and calculates each line at `minutes / 480 × day-rate`.
3. It prints a day/rate breakdown without changing Stripe.
4. With explicit `--create`, it creates a Stripe `send_invoice` draft and one line item per day.
5. Finalizing and sending remain separate approval steps in Stripe.

## Configuration

Noko credentials can be supplied through `NOKO_TOKEN`, `--noko-token`, or the existing `[noko] api_token` setting. Stripe credentials can be supplied through `STRIPE_SECRET_KEY`, `--stripe-key`, or:

```toml
[stripe]
api_key = ""
```

Prefer environment variables or a secret manager for live keys. The existing Notion invoice and Heroku PDF commands remain available during migration; they are not required for the Stripe path.

## Examples

Preview only:

```sh
wrklogr stripe-invoice \
  --customer-id cus_... \
  --project-id 12345 \
  --since 2026-09-01 \
  --until 2026-09-30 \
  --day-rate 900
```

Create a Stripe draft after reviewing the output:

```sh
STRIPE_SECRET_KEY=sk_live_... wrklogr stripe-invoice \
  --customer-id cus_... \
  --project-id 12345 \
  --since 2026-09-01 \
  --until 2026-09-30 \
  --day-rate 900 \
  --days-until-due 30 \
  --create
```

The command prints the Stripe invoice ID and explicitly does not finalize or send it. Review the line items in Stripe, then finalize/send from the Dashboard or a separately approved automation. This makes a hosted Stripe invoice link available without exposing the client to an unreviewed invoice.

## Migration and retirement

During cutover, compare a Stripe draft against the existing Bean PDF output for one or two periods. Once the day grouping, rate, descriptions, and totals are accepted, stop creating new Notion invoices. Keep the legacy Notion/Heroku renderer read-only until historical PDFs are archived and any required links have been migrated. Then remove its credentials and hosting only after a separate retention review.
