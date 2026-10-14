# Stripe invoicing

`wrklogr stripe-invoice` is the replacement path for creating a payable invoice from Noko work. Noko is the source of truth for time, while Stripe owns the customer, invoice, hosted payment page, and payment state. The command asks Noko for billable, not-yet-invoiced entries only, matching Noko's own invoice action.

The command is intentionally draft-first:

1. It reads Noko entries for a project and date range.
2. By default it groups entries by day and calculates each line at `minutes / 480 × day-rate`, rounded to cents.
3. It prints a day/rate breakdown without changing Stripe.
4. With explicit `--create`, it creates a Stripe `send_invoice` draft and one line item per day.
5. Finalizing and sending remain separate approval steps in Stripe.

For historical parity audits only, `--include-invoiced` reads all billable
entries in the period, including entries Noko has already marked invoiced. It
is read-only and cannot be combined with `--create`; normal invoice creation
continues to use only billable, not-yet-invoiced entries.

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

The Stripe account may maintain one-time service Prices for approved day-rate
references. The invoice command still uses explicit invoice-item amounts from
Noko: Price quantities are not the source of truth because legacy quarter-day
rounding, gratis deductions, renderer rounding, and total overrides can change
the final amount per invoice.

To configure the legacy category model, add a Stripe legacy section. The
default category is used when an entry has no recognized tag or configured
user. Category rates override `--day-rate` for that category:

```toml
[stripe.legacy]
default_category = "backend"
# Optional exact compatibility with the old API's untagged roster fallback.
# Omit for logical backend/frontend roster mapping.
user_mapping = "bean"

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

For client-based Notion overrides, add an optional profile keyed by the Noko
project ID. A profile supplies the default rate and can override the legacy
category rates without changing the global defaults:

```toml
[stripe.profiles."606165"]
default_rate = 1000
rounding = "legacy-quarter-day"
default_category = "backend"
user_mapping = "bean"

[stripe.profiles."606165".rates]
backend = 1000
frontend = 900
design = 900
pm = 900
qa = 900
```

The profile is used only when `--day-rate` and `--rounding` are not supplied
explicitly. Explicit flags win. Profile rates apply to legacy category lines;
the client profile is copied into Stripe metadata as `pricing_profile`. This
keeps reusable client pricing separate from invoice-only `--gratis-days`,
`--total-override`, and `--discount-note` adjustments.

The old Notion invoice's `gratis_*` and `total_override` values can be carried
into a draft explicitly. Repeat `--gratis-days category=days` for each gratis
row. `--total-override` adds one final positive or negative adjustment so the
Stripe total matches the legacy total; `--discount-note` is included in that
line's description. These flags are intentionally explicit per invoice rather
than read from Notion:

```sh
wrklogr stripe-invoice \
  --rounding legacy-quarter-day \
  --gratis-days design=0.25 \
  --total-override 5000 \
  --discount-note "courtesy discount" \
  ...
```

Gratis rows are negative Stripe invoice items. Legacy mode also adds an
explicit cents adjustment when needed to preserve the web renderer's final
whole-dollar `Math.round(subTotal)` behavior. A `total_override` supersedes
that rounding adjustment.

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

After an invoice is finalized or paid, reconcile it without changing Stripe:

```sh
wrklogr stripe-invoice-status --invoice-id in_...
```

This reports the Stripe status, paid flag, due/paid amounts, payment timestamp,
and hosted invoice URL. It is read-only and is intended for the cutover
checklist and payment-record reconciliation.

## Migration and retirement

During cutover, compare a Stripe draft against the existing Bean PDF output for one or two periods. Once the day grouping, rate, descriptions, and totals are accepted, stop creating new Notion invoices. Keep the legacy Notion/Heroku renderer read-only until historical PDFs are archived and any required links have been migrated. Then remove its credentials and hosting only after a separate retention review.
