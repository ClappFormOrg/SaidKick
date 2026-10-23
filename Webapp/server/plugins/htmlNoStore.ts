// Rendered HTML carries a per-response CSP nonce, so no cache may store it:
// a cached page would hand every visitor the same nonce. render:response
// fires only for pages the Nuxt renderer produces, so /_nuxt/ assets keep
// their immutable caching.
export default defineNitroPlugin((nitroApp) => {
  nitroApp.hooks.hook('render:response', (response) => {
    response.headers = { ...response.headers, 'cache-control': 'no-store' }
  })
})
