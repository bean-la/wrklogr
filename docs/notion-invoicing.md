# Notion Invoicing Runbook

Generate and populate draft invoice pages in the Notion Invoice Status database from local git history.

## Prerequisites

### Notion setup

1. Create an internal integration at [notion.so/profile/integrations](https://www.notion.so/profile/integrations) with **Read content**, **Update content**, and **Insert content** capabilities
2. Share both the **Invoice Status** database and the **Clients** database with the integration (open each DB → `...` menu → Connections → add your integration)
3. Copy the integration token — it starts with `secret_`

### Config

Add to `wrklogr.toml`:

```toml
[notion]
api_token    = ""           # or leave blank and set NOTION_TOKEN in .env
invoice_db_id = "3176fa6290f3434b85f0738972c0ae75"
clients_db_id = "6859c5c9453d4675a99ea2e12b9c4025"
role          = "backend"   # which rate column to use from Clients DB
                            # options: backend | frontend | design | sr_backend | ios
```

Add to `.env` (preferred over putting the token in the TOML):

```
NOTION_TOKEN=secret_...
```

The Clients DB must have **Noko Project ID** populated for each client — this is the bridge that maps your git sessions to the right client and rate.

### Noko project mapping

Your `wrklogr.toml` also needs `[noko.projects]` entries so sessions can be attributed to a client:

```toml
[noko]
[noko.projects]
"my-repo" = { project_id = 586501 }
"other-repo" = { project_id = 519658 }
```

---

## Monthly workflow — new invoice

Run after the month closes (any time after the 1st).

```bash
# 1. Dry-run to verify hours and computed amount
wrklogr notion-invoice \
  --invoice-number ADV-807 \
  --local-path ~/dev/client-repo \
  --dry-run

# 2. Create the draft page in Notion
wrklogr notion-invoice \
  --invoice-number ADV-807 \
  --local-path ~/dev/client-repo

# 3. Open Notion, review the draft
#    - Attach PDF once generated
#    - Set "Sent" date when sending
#    - Update status: Draft → Sent → Paid
```

The date range defaults to the **previous calendar month** — no `--since`/`--until` needed for the standard case.

### What gets populated automatically

| Field | Source |
|---|---|
| Invoice Number | `--invoice-number` flag |
| Amount | `(total hours) × (rate from Clients DB for configured role)` |
| Billed Dates | previous calendar month (or `--since`/`--until`) |
| Client | relation to Clients DB, matched via Noko project ID |
| Description | month + repos + top commit messages |
| Invoice Status | `Draft` |
| Net | pulled from Clients DB (NET-15, NET-30, etc.) |
| Net Days | pulled from Clients DB |

Fields left for manual entry: Invoice PDF, Sent date, Discount Note, Gratis fields, Payment Received.

---

## Update an existing invoice

Use this when an invoice was created manually in Notion and needs amount/description backfilled.

```bash
# Dry-run first
wrklogr notion-invoice \
  --update --invoice-number ADV-806 \
  --since 2026-04-01 --until 2026-04-30 \
  --local-path ~/dev/hnm-brodie-pet \
  --dry-run

# Write when satisfied
wrklogr notion-invoice \
  --update --invoice-number ADV-806 \
  --since 2026-04-01 --until 2026-04-30 \
  --local-path ~/dev/hnm-brodie-pet
```

The update patches **Amount, Billed Dates, Description, Net, Net Days** only. Invoice Number, Client relation, and Invoice Status are left untouched.

Rate and NET terms are resolved from the client already linked on the Notion page — no extra config needed.

---

## Multiple repos for one client

Pass `--local-path` multiple times. Hours are summed across all repos that share the same Noko project ID.

```bash
wrklogr notion-invoice \
  --invoice-number ADV-808 \
  --local-path ~/dev/client-api \
  --local-path ~/dev/client-web \
  --dry-run
```

---

## Custom date range

Override the default previous-month range with explicit dates:

```bash
wrklogr notion-invoice \
  --invoice-number ADV-809 \
  --since 2026-03-15 --until 2026-04-15 \
  --local-path ~/dev/client-repo
```

---

## Troubleshooting

**`no Notion client for Noko project X — skipping`**
The Clients DB doesn't have a matching "Noko Project ID" for that project. Open the client page in Notion and fill in the field.

**`notion API POST /databases/...: 400`**
Usually a property name mismatch. Check that the Invoice Status DB has properties named exactly: `Invoice Number`, `Amount`, `Billed Dates`, `Client`, `Description`, `Invoice Status`, `Net`, `Net Days`.

**Amount is $0.00**
The rate column for the configured `role` is empty on the client's Notion page, or `role` in config doesn't match any known value.

**`invoice ADV-XXX not found in Notion`**
The `--update` flag queries by invoice number — confirm the "Invoice Number" field in Notion matches exactly (case-sensitive).
