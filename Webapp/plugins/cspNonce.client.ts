// The CSP nonce for <script> and <style> tags that useHead adds in the
// browser. nuxt-security nonces everything the server renders, but a tag
// inserted later (Nuxt's own error page adds an inline script and style)
// would be blocked without it. The nonce is read back from a server-rendered
// element: browsers hide the attribute but keep the .nonce property.
export default defineNuxtPlugin({
  name: 'csp-nonce',
  setup() {
    const nonce = document.querySelector<HTMLElement>('script[nonce]')?.nonce
    const hooks = injectHead()?.hooks
    if (!nonce || !hooks) return
    hooks.hook('tags:resolve', ({ tags }) => {
      for (const tag of tags) {
        if (tag.tag === 'script' || tag.tag === 'style') tag.props.nonce = nonce
      }
    })
  },
})
