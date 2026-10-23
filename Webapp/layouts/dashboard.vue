<template>
  <!--
    Signed-in shell: the public chrome with the dashboard sidebar beside the
    page. It nests inside the default layout rather than repeating the
    masthead, header and footer. The page sits on the surface colour, so the
    white sidebar and cards read as raised without a shadow.

    Only /dashboard/** uses it, and those routes render on the client
    (nuxt.config.ts routeRules), so reading the signed-in user here causes no
    hydration mismatch.
  -->
  <NuxtLayout name="default">
    <div class="bg-brand-surface">
      <div class="mx-auto flex max-w-page flex-col gap-6 px-6 py-8 lg:flex-row lg:gap-8">
        <DashboardSidebar />
        <div class="min-w-0 flex-1">
          <slot />
        </div>
      </div>
    </div>
  </NuxtLayout>
</template>
