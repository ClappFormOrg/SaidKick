import { internalApiBase } from '../../../utils/branding'

// GET /branding/logo: the uploaded instance logo (#158), proxied from the
// API so the browser loads it from the webapp's own origin. The CSP's
// img-src 'self' then covers it even when the API is on another origin; no
// directive is widened.
//
// The bytes are checked again here (PNG signature, size) and always served
// as image/png with nosniff, whatever the upstream response says.

const PNG_SIGNATURE = [0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a]
const MAX_LOGO_BYTES = 512 * 1024
const UPSTREAM_TIMEOUT_MS = 5000

function isPng(bytes: Uint8Array): boolean {
  return bytes.length > PNG_SIGNATURE.length && PNG_SIGNATURE.every((b, i) => bytes[i] === b)
}

export default defineEventHandler(async (event) => {
  const base = internalApiBase(useRuntimeConfig(event), process.env)
  if (!base) throw createError({ statusCode: 502, statusMessage: 'Bad Gateway' })

  let upstream: Response
  try {
    upstream = await fetch(`${base}/api/v1/branding/logo`, { signal: AbortSignal.timeout(UPSTREAM_TIMEOUT_MS) })
  } catch {
    throw createError({ statusCode: 502, statusMessage: 'Bad Gateway' })
  }
  if (upstream.status === 404) throw createError({ statusCode: 404, statusMessage: 'Not Found' })
  if (!upstream.ok) throw createError({ statusCode: 502, statusMessage: 'Bad Gateway' })

  const bytes = new Uint8Array(await upstream.arrayBuffer())
  if (bytes.length > MAX_LOGO_BYTES || !isPng(bytes)) {
    throw createError({ statusCode: 502, statusMessage: 'Bad Gateway' })
  }

  setResponseHeaders(event, {
    'content-type': 'image/png',
    'x-content-type-options': 'nosniff',
    // The page links the logo with ?v=<updated_at>, so a new upload gets a
    // new URL; five minutes bounds how long an old URL serves stale bytes.
    'cache-control': 'public, max-age=300',
  })
  return bytes
})
