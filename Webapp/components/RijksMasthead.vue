<script setup lang="ts">
// The organisation lockup above the application header: the Rijkslint block
// with the organisation name beside it, its first line aligned to the glyph.
// An uploaded instance logo (#158) replaces the lockup outright; it never sits
// inside the Rijkslint block.
//
// It names the organisation that runs the instance, not the product, so it
// does not link anywhere. The product name in the header links home.
withDefaults(defineProps<{
  organisation?: string[]
  ministry?: string[]
}>(), {
  organisation: () => ['Rijksorganisatie voor', 'Informatiehuishouding'],
  ministry: () => ['Ministerie van Binnenlandse Zaken en', 'Koninkrijksrelaties'],
})

const { logo, logoSrc } = useBranding()
</script>

<template>
  <div class="bg-brand-white">
    <div class="mx-auto flex max-w-masthead justify-center px-6 pb-3.5">
      <!-- width and height are the stored intrinsic size, so the browser
           reserves the box before the image arrives. -->
      <img
        v-if="logo"
        :src="logoSrc"
        :width="logo.width"
        :height="logo.height"
        :alt="logo.alt"
        class="mt-4 block h-auto max-h-[72px] w-auto max-w-[280px] object-contain"
      />

      <div v-else class="flex items-start gap-4" data-testid="rijks-lockup">
        <GovnlRijkslint :width="46" :height="104" />
        <p class="m-0 mt-[65px] max-w-[260px] text-brand-primary">
          <span class="block text-sm leading-[1.28]">
            <template v-for="(line, i) in organisation" :key="line">
              <br v-if="i > 0" />{{ line }}
            </template>
          </span>
          <span class="mt-[3px] block text-[0.75rem] italic leading-[1.28]">
            <template v-for="(line, i) in ministry" :key="line">
              <br v-if="i > 0" />{{ line }}
            </template>
          </span>
        </p>
      </div>
    </div>
  </div>
</template>
