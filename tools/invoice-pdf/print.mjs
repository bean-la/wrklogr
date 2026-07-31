#!/usr/bin/env node
/**
 * Render a Notion invoice page to PDF via the bean-invoicing Nuxt UI + Puppeteer.
 * Ported from _ext/bean-invoicing-api/src/lib/puppeteer.ts with auth key support.
 *
 * Requires the invoicing web app running (Heroku or local nuxt from _ext/bean-invoicing-web).
 *
 * Usage:
 *   node print.mjs --page-id <notion-page-uuid> -o invoice.pdf
 *
 * Env:
 *   BEAN_INVOICING_RENDER_URL  Base URL (default https://bean-invoicing.herokuapp.com)
 *   BEAN_INVOICING_KEY         Access key query param (default bababooey)
 *   INVOICING_CONTACT_EMAIL    Footer email override
 */

import fs from 'node:fs'
import path from 'node:path'
import { fileURLToPath, pathToFileURL } from 'node:url'
import puppeteer from 'puppeteer'
import { PDFDocument } from 'pdf-lib'

const __dirname = path.dirname(fileURLToPath(import.meta.url))

function parseArgs(argv) {
  const opts = { pageId: '', out: '', section: '' }
  for (let i = 2; i < argv.length; i++) {
    const a = argv[i]
    if (a === '--page-id' && argv[i + 1]) {
      opts.pageId = argv[++i]
    } else if (a === '-o' && argv[i + 1]) {
      opts.out = argv[++i]
    } else if (a === '--section' && argv[i + 1]) {
      opts.section = argv[++i]
    } else if (a === '--help' || a === '-h') {
      opts.help = true
    }
  }
  return opts
}

function authQuery() {
  const key = process.env.BEAN_INVOICING_KEY || 'bababooey'
  return key ? `?key=${encodeURIComponent(key)}` : ''
}

function renderBaseURL() {
  const base = (
    process.env.BEAN_INVOICING_RENDER_URL ||
    process.env.BEAN_INVOICING_URL ||
    'https://bean-invoicing.herokuapp.com'
  ).replace(/\/$/, '')
  return base
}

/**
 * @param {string} pageId Notion invoice page id (with dashes)
 * @param {string} [section] optional "invoice" | "entries"
 */
export async function printInvoicePDF(pageId, section) {
  const slug = pageId.replace(/-/g, '')
  const base = renderBaseURL()
  const qs = authQuery()
  const siteUrl = 'bean.la'
  const email = process.env.INVOICING_CONTACT_EMAIL || 'AR@bean.la'

  const browser = await puppeteer.launch({
    headless: true,
    args: ['--no-sandbox', '--disable-setuid-sandbox'],
  })
  const page = await browser.newPage()
  const pageUrl = `${base}/${slug}`
  const pdfs = {}

  try {
    await page.goto(`${pageUrl}/invoice${qs}`, { waitUntil: 'networkidle0', timeout: 120000 })
    await page.emulateMediaType('print')
    pdfs.invoice = await page.pdf({
      format: 'letter',
      preferCSSPageSize: true,
      displayHeaderFooter: true,
      headerTemplate: '<div></div>',
      footerTemplate: `
      <div class="footer" style="font-size: 8px; font-family: Arial, sans-serif; padding-top: 13px; width: 100%; position: relative;">
        <div style="position: absolute; left: 26px; bottom: 8px;">${siteUrl}</div>
        <div style="position: absolute; right: 26px; bottom: 8px;">${email}</div>
      </div>
    `,
    })

    await page.goto(`${pageUrl}/entries${qs}`, { waitUntil: 'networkidle0', timeout: 120000 })
    await page.emulateMediaType('print')
    pdfs.entries = await page.pdf({
      format: 'letter',
      preferCSSPageSize: true,
    })
  } finally {
    await browser.close()
  }

  if (section === 'invoice' || section === 'entries') {
    return pdfs[section]
  }

  const merged = await PDFDocument.create()
  const invoiceDoc = await PDFDocument.load(pdfs.invoice)
  const [invoicePage] = await merged.copyPages(invoiceDoc, [0])
  merged.addPage(invoicePage)

  const reportDoc = await PDFDocument.load(pdfs.entries)
  for (let i = 0; i < reportDoc.getPageCount(); i++) {
    const [reportPage] = await merged.copyPages(reportDoc, [i])
    merged.addPage(reportPage)
  }

  return Buffer.from(await merged.save())
}

async function main() {
  const opts = parseArgs(process.argv)
  if (opts.help || !opts.pageId) {
    console.error(`Usage: node print.mjs --page-id <notion-page-id> -o <output.pdf>`)
    process.exit(opts.help ? 0 : 1)
  }

  const pdf = await printInvoicePDF(opts.pageId, opts.section)
  if (opts.out) {
    fs.writeFileSync(opts.out, pdf)
    console.error(`wrote ${opts.out} (${pdf.length} bytes)`)
  } else {
    process.stdout.write(pdf)
  }
}

const isMain = process.argv[1] && import.meta.url === pathToFileURL(path.resolve(process.argv[1])).href
if (isMain) {
  main().catch(err => {
    console.error(err)
    process.exit(1)
  })
}
