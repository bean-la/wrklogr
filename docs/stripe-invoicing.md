# Stripe invoicing

`wrklogr stripe-invoice` is the replacement path for creating a payable invoice from Noko work. Noko is the source of truth for time, while Stripe owns the customer, invoice, hosted payment page, and payment state. The command asks Noko for billable, not-yet-invoiced entries only, matching Noko's own invoice action.

The command is intentionally draft-first:

1. It reads Noko entries for a project and date range.
2. By default it groups entries by day and calculates each line at `minutes / 480 × day-rate`, rounded to cents.
3. It prints a day/rate breakdown without changing Stripe.
4. With explicit `--create`, it creates a Stripe `send_invoice` draft and one line item per day.
5. Finalizing and sending remain separate approval steps in Stripe.

For compatibility with the retired Bean invoice API, pass
`--rounding legacy-quarter-day`. This collapses the period into a summary line,
converts minutes to eight-hour days, rounds the days to the nearest 0.25, and
then multiplies by the day rate. The default `daily-cents` mode is retained for
the more detailed Noko day breakdown.

Created drafts carry a deterministic fingerprint of the Noko project, period,
rate, and entry IDs. Before creating, wrklogr checks the customer’s draft and
open Stripe invoices for the same fingerprint and reuses the existing invoice
instead of creating a duplicate.

## Configuration

Noko credentials can be supplied through `NOKO_TOKEN`, `--noko-token`, or the existing `[noko] api_token` setting. Stripe credentials can be supplied through `STRIPE_SECRET_KEY`, `--stripe-key`, or:

```toml
[stripe]
api_key = ""
```

Prefer environment variables or a secret manager for live keys. The existing Notion invoice and Heroku PDF commands remain available during migration; they are not required for the Stripe path.

To configure the legacy category model, add a Stripe legacy section. The
default category is used when an entry has no recognized tag or configured
user. Category rates override `--day-rate` for that category:

```toml
[stripe.legacy]
default_category = "backend"

[stripe.legacy.rates]
backend = 850
frontend = 850
design = 1000
pm = 850
qa = 850

[stripe.legacy.users]
backend = ["backend@example.com"]
frontend = ["frontend@example.com"]
design = ["designer@example.com"]
```

Entries with `development`, `QA`, `design`, or `PM` Noko tags are classified
using the Bean API rules. Generated Stripe line metadata preserves raw minutes,
category, rounded days, rate, and Noko entry IDs.

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

Legacy-compatible preview:

```sh
wrklogr stripe-invoice \
  --customer-id cus_... \
  --project-id 12345 \
  --since 2026-09-01 \
  --until 2026-09-30 \
  --day-rate 850 \
  --rounding legacy-quarter-day
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
