#!/usr/bin/env node
// CSP crawl (#137): proves the nonce-based Content-Security-Policy breaks no
// page of the production build.
//
//   node scripts/csp-crawl.mjs
//
// 1. Starts a stub API on a free port and builds the webapp against it, so
//    the API origin lands in connect-src the way a cross-origin deployment
//    would.
// 2. Starts the built server (.output/server/index.mjs).
// 3. Opens every route in pages/ in Chromium, dynamic segments filled with
//    fixture values and the dashboard behind a stubbed session, first as a
//    full page load and then as a client-side navigation (which loads route
//    chunks through script-inserted modulepreload links).
// 4. Fails on any securitypolicyviolation event or console CSP error, on an
//    HTML response whose policy or security headers differ from
//    utils/securityHeaders.ts, on a <script>, <style>, modulepreload or
//    script preload element without the response's nonce, on HTML that is not
//    Cache-Control: no-store, and on a nonce seen twice.
// 5. Runs with a custom brand and logo set (#158): every page must carry the
//    injected palette in a nonced <style> (server-rendered pages and the
//    client-only dashboard alike), and the header logo must load from the
//    webapp's own /branding/logo route under img-src 'self'.
//
// Needs Playwright's Chromium: npx playwright install chromium

import { spawn } from 'node:child_process'
import { readdirSync } from 'node:fs'
import { createServer } from 'node:http'
import { deflateSync } from 'node:zlib'
import { basename, dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { chromium } from 'playwright'
import { NONCE_SOURCE, apiOrigin, securityHeaders } from '../utils/securityHeaders.ts'

const ROOT = join(dirname(fileURLToPath(import.meta.url)), '..')
const HOST = '127.0.0.1'
const SETTLE_MS = 750

// ── Fixtures ──────────────────────────────────────────────────────────────────
const ID = '00000000-0000-4000-8000-000000000001'
const ORG_ID = '00000000-0000-4000-8000-000000000002'
const VERSION_ID = '00000000-0000-4000-8000-000000000003'
const APP_SLUG = 'csp-fixture-app'
const ORG_SLUG = 'csp-fixture-org'
const REPO = 'example/csp-fixture-app'
const NOW = new Date().toISOString()

// Values for dynamic segments, by the page directory and parameter name. A
// new dynamic page without an entry here fails the crawl until one is added.
const PARAM_FIXTURES = {
  'catalogus/slug': APP_SLUG,
  'dashboard/apps/slug': APP_SLUG,
  'dashboard/audit/id': ID,
  'dashboard/clusters/id': ID,
  'dashboard/deployments/id': ID,
  'dashboard/org/slug': ORG_SLUG,
}

// Query strings a page needs to render instead of throwing.
const QUERY_FIXTURES = {
  '/dashboard/deployments/new':
    `?app_id=${ID}&app_version_id=${VERSION_ID}&app_slug=${APP_SLUG}&app_name=CSP%20Fixture`,
}

// Error pages: Nuxt renders one on the server for an unknown path and in the
// browser when a page throws, and its error page adds an inline script and
// style through useHead. Last in the crawl so they cannot affect other pages.
const MISSING_ID = '00000000-0000-4000-8000-00000000dead'
// error.vue titles (Webapp/error.vue COPY), plus Nuxt's default "404 - ..."
// fallback in case the branded page fails to render.
const ERROR_TITLE =
  /^(\d{3} - |(Ongeldig verzoek|Niet ingelogd|Geen toegang|Pagina niet gevonden|Er ging iets mis|Tijdelijk niet beschikbaar) — )/

const ERROR_ROUTES = [
  { pattern: '(404, server-rendered)', url: '/csp-crawl-missing', expectError: true },
  { pattern: '(404, client-rendered)', url: `/dashboard/deployments/${MISSING_ID}`, expectError: true },
]

const ME = {
  id: ID,
  email: 'csp-crawl@example.org',
  name: 'CSP Crawl',
  roles: ['developer', 'deployer', 'cluster_admin', 'platform_admin', 'auditor', 'org_admin'],
  org_slug: ORG_SLUG,
  org_name: 'CSP Fixture Org',
}
const APP_CARD = {
  id: ID, slug: APP_SLUG, vendor: 'CSP Fixture', state: 'published', github_repo: REPO,
  display_name: 'CSP Fixture App', categories: ['documenten'], risk_class: 'minimal',
  license: 'EUPL-1.2', last_published_at: NOW, auto_validation_state: 'passed', audit_approved: true,
}
const APP_DETAIL = {
  ...APP_CARD,
  owning_organization_id: ORG_ID, owning_user_id: ID, github_installation_id: 1,
  github_default_branch: 'main', repo_link_state: 'linked', created_at: NOW, updated_at: NOW,
  chart_version: '1.0.0', installs: 1, current_published_version_id: VERSION_ID,
  values_schema: { type: 'object', properties: { replicaCount: { type: 'integer', default: 1 } } },
  manifest: {
    metadata: { name: APP_SLUG, display_name: 'CSP Fixture App', vendor: 'CSP Fixture', contact: 'csp@example.org', license: 'EUPL-1.2' },
    spec: {
      categories: ['documenten'],
      ai: { risk_class: 'minimal' },
      // Fetched from raw.githubusercontent.com (connect-src and img-src).
      demo: { description: 'docs/description.md', screenshots: ['docs/screenshot.png'] },
      audit: { dpia: 'docs/dpia.md' },
    },
  },
}
APP_DETAIL.my_deployments = [{ deployment_id: ID, availability: 'available', url: 'https://csp-fixture.example.org' }]
const MY_APPS = {
  items: [
    {
      deployment_id: ID, app_slug: APP_SLUG, display_name: 'CSP Fixture App', categories: ['documenten'],
      cluster_name: 'csp-fixture', availability: 'available', access: 'web', url: 'https://csp-fixture.example.org',
      maintainer: { name: 'CSP Crawl', email: 'csp-crawl@example.org' },
    },
    {
      deployment_id: VERSION_ID, app_slug: APP_SLUG, display_name: 'CSP Fixture API', categories: [],
      availability: 'preparing', access: 'none', maintainer: { vendor_name: 'CSP Fixture', vendor_contact: 'csp@example.org' },
    },
  ],
  total: 2, page: 1, page_size: 100,
}
const CLUSTER = {
  id: ID, name: 'csp-fixture', description: 'CSP fixture cluster', scope: 'org', state: 'ready',
  agent_version: '1.0.0', last_heartbeat_at: NOW, registered_at: NOW,
  orchestrator_cluster_name: 'csp-fixture', owning_organization_id: ORG_ID,
}
const DEPLOYMENT = {
  id: ID, app_id: ID, app_version_id: VERSION_ID, app_slug: APP_SLUG, cluster_id: ID,
  namespace: 'csp-fixture', state: 'syncing', last_state_change_at: NOW, created_at: NOW,
}
const SUBMISSION = {
  id: ID, auto_validation_state: 'passed', form_version: '1', created_at: NOW,
  evidence_files: [{ logical_name: 'dpia', repo_path: 'docs/dpia.md', content_sha256: '0'.repeat(64), size_bytes: 10, content_type: 'text/markdown' }],
  app: { slug: APP_SLUG, name: 'CSP Fixture App', owning_organization_id: ORG_ID, risk_class: 'minimal' },
  version: { git_ref: 'v1.0.0', git_sha: 'a'.repeat(40), state: 'submitted' },
}
const DISCUSSION = {
  id: ID, number: 1, title: 'CSP fixture', url: 'https://github.com/example/discussions/1',
  author: 'csp', author_avatar: '', category: 'General', created_at: NOW, updated_at: NOW,
  comment_count: 0, upvote_count: 0, excerpt: 'Fixture discussion',
}

// A custom brand (#158) so the crawl covers the injected palette and the
// uploaded logo. The values pass the API's contrast check.
const BRAND_CTA = '#0b5cad'
const BRANDING = {
  colors: { cta: BRAND_CTA, primary: '#1a1a6e', secondary: '#eef3ff', surface: '#f7f7f7', body_text: '#111111', border: '#d0d0d0' },
  tokens: {
    cta: BRAND_CTA, cta_hover: '#094a8a', cta_active: '#073768', cta_subtle: '#dae7f3',
    primary: '#1a1a6e', primary_hover: '#151558', primary_active: '#101042', primary_subtle: '#dddde9',
    secondary: '#eef3ff', surface: '#f7f7f7', body_text: '#111111', border: '#d0d0d0',
    wedge_start: '#101042', wedge_mid: '#1a1a6e', wedge_end: '#21218b',
  },
  is_default: false,
  logo: { url: '/api/v1/branding/logo', width: 16, height: 16, alt: 'CSP fixture logo' },
  contrast_failures: [],
  updated_at: NOW,
}

const API_ROUTES = [
  [/^\/api\/v1\/me$/, () => ME],
  [/^\/api\/v1\/apps$/, () => ({ items: [APP_CARD], total: 1 })],
  [/^\/api\/v1\/apps\/[^/]+$/, () => APP_DETAIL],
  [/^\/api\/v1\/apps\/[^/]+\/versions$/, () => ({ items: [{ id: VERSION_ID, git_ref: 'v1.0.0', git_sha: 'a'.repeat(40), state: 'published', created_at: NOW }], total: 1 })],
  [/^\/api\/v1\/clusters$/, () => ({ items: [CLUSTER], total: 1 })],
  [/^\/api\/v1\/clusters\/[^/]+$/, () => CLUSTER],
  [/^\/api\/v1\/clusters\/[^/]+\/access$/, () => ({ items: [{ cluster_id: ID, organization_id: ORG_ID, organization_name: 'CSP Fixture Org', granted_at: NOW, granted_by: ID }] })],
  [/^\/api\/v1\/deployments$/, () => ({ items: [DEPLOYMENT], total: 1 })],
  [/^\/api\/v1\/deployments\/[^/]+$/, () => DEPLOYMENT],
  [/^\/api\/v1\/audit-submissions$/, () => ({ items: [SUBMISSION], total: 1, page: 1, page_size: 50 })],
  [/^\/api\/v1\/audit-submissions\/[^/]+$/, () => SUBMISSION],
  [/^\/api\/v1\/audit-submissions\/[^/]+\/comments$/, () => ({ items: [], total: 0 })],
  [/^\/api\/v1\/orgs$/, () => ({ items: [{ slug: ORG_SLUG, display_name: 'CSP Fixture Org' }] })],
  [/^\/api\/v1\/orgs\/[^/]+\/members$/, () => ({ items: [{ id: ID, email: ME.email, display_name: ME.name, roles: ME.roles, created_at: NOW }] })],
  [/^\/api\/v1\/api-keys$/, () => ({ items: [{ id: ID, name: 'csp', prefix: 'sk_csp', created_at: NOW }], total: 1 })],
  [/^\/api\/v1\/advisories$/, () => ({ items: [] })],
  [/^\/api\/v1\/my-apps$/, () => MY_APPS],
  [/^\/api\/v1\/community\/discussions$/, () => ({ items: [DISCUSSION] })],
  [/^\/api\/v1\/branding$/, () => BRANDING],
]

// 1x1 transparent PNG for the screenshot fixture.
const PNG = Buffer.from('iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNkYAAAAAYAAjCB0C8AAAAASUVORK5CYII=', 'base64')

// 16x16 PNG for the brand logo, built here so it is a real image.
function solidPng(size) {
  const crcTable = Array.from({ length: 256 }, (_, n) => {
    let c = n
    for (let k = 0; k < 8; k++) c = c & 1 ? 0xedb88320 ^ (c >>> 1) : c >>> 1
    return c >>> 0
  })
  const crc = (buf) => {
    let c = 0xffffffff
    for (const b of buf) c = crcTable[(c ^ b) & 0xff] ^ (c >>> 8)
    return (c ^ 0xffffffff) >>> 0
  }
  const chunk = (type, data) => {
    const len = Buffer.alloc(4)
    len.writeUInt32BE(data.length)
    const body = Buffer.concat([Buffer.from(type, 'ascii'), data])
    const sum = Buffer.alloc(4)
    sum.writeUInt32BE(crc(body))
    return Buffer.concat([len, body, sum])
  }
  const ihdr = Buffer.alloc(13)
  ihdr.writeUInt32BE(size, 0)
  ihdr.writeUInt32BE(size, 4)
  ihdr.set([8, 2, 0, 0, 0], 8) // 8-bit RGB
  const row = Buffer.concat([Buffer.from([0]), Buffer.alloc(size * 3, 0x40)])
  const raw = Buffer.concat(Array.from({ length: size }, () => row))
  return Buffer.concat([
    Buffer.from([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a]),
    chunk('IHDR', ihdr), chunk('IDAT', deflateSync(raw)), chunk('IEND', Buffer.alloc(0)),
  ])
}
const LOGO_PNG = solidPng(16)
let logoRequests = 0

// ── Stub API ──────────────────────────────────────────────────────────────────
const unstubbed = new Set()

function startStubApi() {
  const server = createServer((req, res) => {
    // credentials: 'include' needs the exact origin echoed back.
    const cors = {
      'access-control-allow-origin': req.headers.origin || '*',
      'access-control-allow-credentials': 'true',
      'access-control-allow-headers': req.headers['access-control-request-headers'] || '*',
      'access-control-allow-methods': 'GET, POST, PUT, PATCH, DELETE, OPTIONS',
    }
    if (req.method === 'OPTIONS') {
      res.writeHead(204, cors).end()
      return
    }
    const path = new URL(req.url, 'http://stub').pathname
    if (/^\/api\/v1\/deployments\/[^/]+\/events$/.test(path)) {
      res.writeHead(200, { ...cors, 'content-type': 'text/event-stream', 'cache-control': 'no-store' })
      res.write('retry: 600000\n\n')
      res.write(`event: state\ndata: ${JSON.stringify({ state: 'syncing' })}\n\n`)
      res.end(`event: end\ndata: ${JSON.stringify({ state: 'ready' })}\n\n`)
      return
    }
    if (path === '/api/v1/branding/logo') {
      logoRequests++
      res.writeHead(200, { ...cors, 'content-type': 'image/png' })
      res.end(LOGO_PNG)
      return
    }
    const route = !path.includes(MISSING_ID) && API_ROUTES.find(([re]) => re.test(path))
    if (!route) {
      if (!path.includes(MISSING_ID)) unstubbed.add(`${req.method} ${path}`)
      res.writeHead(404, { ...cors, 'content-type': 'application/json' })
      res.end(JSON.stringify({ code: 'not_found', message: 'not stubbed' }))
      return
    }
    res.writeHead(200, { ...cors, 'content-type': 'application/json' })
    res.end(JSON.stringify(route[1]()))
  })
  return new Promise((resolve) => server.listen(0, HOST, () => resolve(server)))
}

// ── Build and server ──────────────────────────────────────────────────────────
function run(command, env) {
  return new Promise((resolve, reject) => {
    const child = spawn(command, { cwd: ROOT, env, shell: true, stdio: 'inherit' })
    child.on('exit', code => (code === 0 ? resolve() : reject(new Error(`${command} exited with ${code}`))))
  })
}

function freePort() {
  return new Promise((resolve) => {
    const s = createServer().listen(0, HOST, () => {
      const { port } = s.address()
      s.close(() => resolve(port))
    })
  })
}

async function startApp(port, apiBase) {
  const child = spawn(process.execPath, ['.output/server/index.mjs'], {
    cwd: ROOT,
    // The server reaches the API on its internal origin for the brand and
    // the logo proxy, as the hub chart's API_BASE_INTERNAL_URL does.
    env: { ...process.env, HOST, PORT: String(port), NUXT_PUBLIC_API_BASE: apiBase, API_BASE_INTERNAL_URL: apiBase },
    stdio: ['ignore', 'inherit', 'inherit'],
  })
  const origin = `http://${HOST}:${port}`
  for (let i = 0; i < 120; i++) {
    try {
      await fetch(origin)
      return { child, origin }
    } catch {
      await new Promise(r => setTimeout(r, 250))
    }
  }
  child.kill()
  throw new Error('webapp server did not start')
}

// ── Routes from pages/ ────────────────────────────────────────────────────────
function pageRoutes(dir = join(ROOT, 'pages'), segments = []) {
  const routes = []
  for (const entry of readdirSync(dir, { withFileTypes: true })) {
    if (entry.isDirectory()) {
      routes.push(...pageRoutes(join(dir, entry.name), [...segments, entry.name]))
      continue
    }
    if (!entry.name.endsWith('.vue')) continue
    const name = basename(entry.name, '.vue')
    const parts = name === 'index' ? segments : [...segments, name]
    const pattern = '/' + parts.join('/')
    const url = '/' + parts.map((part, i) => {
      const param = /^\[\.{0,3}(\w+)\]$/.exec(part)?.[1]
      if (!param) return part
      const key = [...parts.slice(0, i), param].join('/')
      const value = PARAM_FIXTURES[key]
      if (!value) throw new Error(`no fixture for ${pattern} (${key}); add one to PARAM_FIXTURES`)
      return value
    }).join('/')
    routes.push({ pattern, url: url + (QUERY_FIXTURES[url] ?? '') })
  }
  return routes.sort((a, b) => a.pattern.localeCompare(b.pattern))
}

// ── HTML response checks ──────────────────────────────────────────────────────
function attr(tag, name) {
  const match = new RegExp(`\\s${name}(?:=("[^"]*"|'[^']*'|[^\\s>]+))?(?=[\\s/>])`, 'i').exec(tag)
  if (!match) return undefined
  return (match[1] ?? '').replace(/^["']|["']$/g, '')
}

// Elements that must carry the nonce: every <script> and <style>, every
// modulepreload link and every preload link for a script.
function nonceCoverage(html, nonce) {
  const missing = []
  let counted = 0
  for (const [tag, name] of html.matchAll(/<(script|style|link)\b[^>]*>/gi)) {
    const kind = name.toLowerCase()
    if (kind === 'link') {
      const rel = (attr(tag, 'rel') ?? '').toLowerCase().split(/\s+/)
      const as = (attr(tag, 'as') ?? '').toLowerCase()
      if (!rel.includes('modulepreload') && !(rel.includes('preload') && as === 'script')) continue
    }
    counted++
    if (attr(tag, 'nonce') !== nonce) missing.push(tag.slice(0, 120))
  }
  return { counted, missing }
}

function checkHtmlResponse({ status, headers, body }, expected, label, failures, nonces) {
  const csp = headers.get('content-security-policy') ?? ''
  const nonce = /'nonce-([^']+)'/.exec(csp)?.[1]
  if (!nonce) {
    failures.push(`${label}: no nonce in Content-Security-Policy "${csp}"`)
    return null
  }
  if (nonces.has(nonce)) failures.push(`${label}: nonce ${nonce} was already served`)
  nonces.add(nonce)

  // nuxt-security ends every directive with ";".
  const wantCsp = expected['Content-Security-Policy'].replaceAll(NONCE_SOURCE, `'nonce-${nonce}'`) + ';'
  if (csp !== wantCsp) failures.push(`${label}: Content-Security-Policy\n    got  ${csp}\n    want ${wantCsp}`)
  for (const [name, want] of Object.entries(expected)) {
    if (name === 'Content-Security-Policy') continue
    if (headers.get(name) !== want) failures.push(`${label}: ${name} is "${headers.get(name)}", want "${want}"`)
  }
  if (headers.get('cache-control') !== 'no-store') {
    failures.push(`${label}: Cache-Control is "${headers.get('cache-control')}", want "no-store"`)
  }
  if (headers.get('x-powered-by')) failures.push(`${label}: X-Powered-By is set`)

  const { counted, missing } = nonceCoverage(body, nonce)
  for (const tag of missing) failures.push(`${label} (${status}): element without the response nonce: ${tag}`)
  return { nonce, counted }
}

// ── Brand checks ──────────────────────────────────────────────────────────────
function checkBrand(state, label, failures) {
  if (state.styles !== 1) failures.push(`${label}: ${state.styles} brand <style> elements, want 1`)
  else if (!state.nonce) failures.push(`${label}: the brand <style> has no nonce`)
  if (state.cta !== BRAND_CTA) failures.push(`${label}: --brand-cta is "${state.cta}", want ${BRAND_CTA}`)
  if (state.header && !state.logo) failures.push(`${label}: the header shows no uploaded logo`)
  if (state.logo && !state.logo.loaded) failures.push(`${label}: the header logo did not load`)
  if (state.logo && state.logo.alt !== BRANDING.logo.alt) failures.push(`${label}: logo alt is "${state.logo.alt}"`)
}

// ── Crawl ─────────────────────────────────────────────────────────────────────
async function main() {
  const stub = await startStubApi()
  const apiBase = `http://${HOST}:${stub.address().port}`
  const appPort = await freePort()

  console.log(`[csp-crawl] building against stub API ${apiBase}`)
  await run('npx nuxi build', { ...process.env, API_BASE_URL: apiBase, NUXT_PUBLIC_API_BASE: apiBase })

  const { child, origin } = await startApp(appPort, apiBase)
  const expected = securityHeaders({ apiOrigin: apiOrigin(apiBase, origin) })
  const routes = [...pageRoutes(), ...ERROR_ROUTES]
  const failures = []
  const nonces = new Set()
  const violations = []
  const pageErrors = []
  let githubRaw = 0
  let current = ''

  const browser = await chromium.launch()
  try {
    const context = await browser.newContext()
    // The crawl stays offline: GitHub raw content is fulfilled locally and
    // any other foreign request is aborted. CSP is enforced before routing,
    // so a blocked source still raises its violation.
    await context.route(url => !url.href.startsWith(origin) && !url.href.startsWith(apiBase), (route) => {
      const url = new URL(route.request().url())
      if (url.origin !== 'https://raw.githubusercontent.com') return route.abort()
      githubRaw++
      if (url.pathname.endsWith('.png')) return route.fulfill({ status: 200, contentType: 'image/png', body: PNG })
      return route.fulfill({ status: 200, contentType: 'text/plain', headers: { 'access-control-allow-origin': '*' }, body: '# CSP fixture\n\nDescription.' })
    })
    await context.exposeBinding('__cspViolation', (_source, v) => violations.push({ route: current, ...v }))
    await context.addInitScript(() => {
      document.addEventListener('securitypolicyviolation', (e) => {
        window.__cspViolation({
          directive: e.effectiveDirective, blocked: e.blockedURI, source: e.sourceFile,
          line: e.lineNumber, sample: e.sample, page: location.pathname,
        })
      })
    })

    const page = await context.newPage()
    page.on('console', (msg) => {      if (msg.type() === 'error' && /content[- ]security[- ]policy/i.test(msg.text())) {
        violations.push({ route: current, console: msg.text() })
      }
    })
    page.on('pageerror', err => pageErrors.push(`${current}: ${err.message}`))

    const settle = async () => {
      await page.waitForLoadState('networkidle', { timeout: 5000 }).catch(() => {})
      await page.waitForTimeout(SETTLE_MS)
    }
    const hydrated = () => page.evaluate(() => Boolean(document.querySelector('#__nuxt')?.__vue_app__))
    // The injected palette and header logo as the browser sees them. The
    // nonce attribute is hidden once parsed; the .nonce property keeps it.
    const brandState = () => page.evaluate(() => {
      const style = document.querySelector('style#brand-palette')
      const logo = document.querySelector('header img[src^="/branding/logo"]')
      return {
        styles: document.querySelectorAll('style#brand-palette').length,
        nonce: Boolean(style?.nonce),
        cta: getComputedStyle(document.documentElement).getPropertyValue('--brand-cta').trim(),
        header: Boolean(document.querySelector('header')),
        logo: logo ? { loaded: logo.complete && logo.naturalWidth > 0, alt: logo.alt } : null,
      }
    })

    // Pass 1: full page loads.
    const report = []
    for (const { pattern, url, expectError = false } of routes) {
      current = `load ${pattern}`
      const response = await page.goto(origin + url, { waitUntil: 'load' })
      if (!response) {
        failures.push(`${current}: no response`)
        continue
      }
      const body = await response.text()
      const checked = checkHtmlResponse(
        { status: response.status(), headers: new Headers(await response.allHeaders()), body },
        expected, current, failures, nonces,
      )
      // Server-rendered pages carry the palette in the HTML itself; the
      // client-only dashboard adds it in the browser.
      if (!expectError && !pattern.startsWith('/dashboard') && !/<style[^>]*id="brand-palette"/.test(body)) {
        failures.push(`${current}: the server-rendered HTML has no brand <style>`)
      }
      await settle()
      if (!(await hydrated())) failures.push(`${current}: the Vue app did not mount`)
      const title = await page.title()
      if (expectError !== ERROR_TITLE.test(title)) {
        failures.push(`${current}: ${expectError ? 'expected an error page' : 'rendered an error page'}, title "${title}"`)
      }
      if (!expectError) checkBrand(await brandState(), current, failures)
      const landed = new URL(page.url()).pathname
      if (pattern.startsWith('/dashboard') && landed === '/inloggen') {
        failures.push(`${current}: redirected to /inloggen; the stubbed session did not hold`)
      }
      report.push({ pattern, status: response.status(), landed, elements: checked?.counted ?? 0 })
    }

    // Pass 2: client-side navigation from a loaded app, which pulls route
    // chunks in through script-inserted modulepreload links.
    for (const start of ['/', '/dashboard']) {
      current = `load ${start}`
      await page.goto(origin + start, { waitUntil: 'load' })
      await settle()
      for (const { pattern, url } of routes) {
        current = `navigate ${start} -> ${pattern}`
        await page.evaluate(
          to => document.querySelector('#__nuxt').__vue_app__.config.globalProperties.$router.push(to),
          url,
        ).catch(err => pageErrors.push(`${current}: ${err.message}`))
        await settle()
      }
    }

    // A fresh nonce per response: the same URL twice gives two nonces.
    for (const { pattern, url } of routes) {
      for (let i = 0; i < 2; i++) {
        const res = await fetch(origin + url, { headers: { accept: 'text/html' } })
        checkHtmlResponse(
          { status: res.status, headers: res.headers, body: await res.text() },
          expected, `fetch ${pattern} #${i + 1}`, failures, nonces,
        )
      }
    }

    console.log('\n[csp-crawl] routes (full load):')
    for (const r of report) {
      console.log(`  ${String(r.status).padEnd(4)} ${r.pattern.padEnd(42)} ${String(r.elements).padStart(3)} nonced elements${r.landed !== r.pattern && !r.pattern.includes('[') ? `  -> ${r.landed}` : ''}`)
    }
  } finally {
    await browser.close()
    child.kill()
    stub.closeAllConnections()
    stub.close()
  }

  for (const v of violations) failures.push(`CSP violation (${v.route}): ${JSON.stringify(v)}`)
  if (logoRequests === 0) failures.push('the logo was never requested through /branding/logo')
  console.log(`\n[csp-crawl] ${routes.length} routes, ${routes.length * 3} navigations, ${nonces.size} distinct nonces, ${violations.length} CSP violations, ${githubRaw} GitHub raw requests served, ${logoRequests} logo requests proxied`)
  console.log(`[csp-crawl] served policy: ${expected['Content-Security-Policy'].replaceAll(NONCE_SOURCE, "'nonce-<per response>'")};`)
  if (unstubbed.size) console.log(`[csp-crawl] note: unstubbed API calls (404): ${[...unstubbed].join(', ')}`)
  if (pageErrors.length) console.log(`[csp-crawl] note: page errors (not CSP):\n  ${[...new Set(pageErrors)].join('\n  ')}`)
  if (failures.length) {
    console.error(`\n[csp-crawl] FAILED (${failures.length}):\n  ${failures.join('\n  ')}`)
    process.exit(1)
  }
  console.log('[csp-crawl] PASSED')
}

main().catch((err) => {
  console.error(err)
  process.exit(1)
})
