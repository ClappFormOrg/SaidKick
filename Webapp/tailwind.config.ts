import type { Config } from 'tailwindcss'
import plugin from 'tailwindcss/plugin'

// Semantic colours (#156). Each one resolves through a `--brand-*` custom
// property defined in assets/css/govnl.css, so a brand can recolour the site
// at runtime. Prefer these in markup (bg-brand-cta, text-brand-primary,
// border-brand-border); use the `govnl-*` ramps only for feedback colours and
// greys, which stay literal on purpose.
//
// Opacity modifiers (bg-brand-cta/50) do not work on var() colours. If one is
// ever needed, move these tokens to the `rgb(var(--x) / <alpha-value>)` form.
const brandColors = {
  cta: {
    DEFAULT: 'var(--brand-cta)',
    hover: 'var(--brand-cta-hover)',
    active: 'var(--brand-cta-active)',
    subtle: 'var(--brand-cta-subtle)',
  },
  primary: {
    DEFAULT: 'var(--brand-primary)',
    hover: 'var(--brand-primary-hover)',
    active: 'var(--brand-primary-active)',
    subtle: 'var(--brand-primary-subtle)',
  },
  secondary: 'var(--brand-secondary)',
  surface: 'var(--brand-surface)',
  body: 'var(--brand-body)',
  border: 'var(--brand-border)',
  white: 'var(--brand-white)',
} as const

// The component classes below read the same variables, so they follow a
// saved brand too.
const B = {
  cta: 'var(--brand-cta)',
  ctaHover: 'var(--brand-cta-hover)',
  ctaActive: 'var(--brand-cta-active)',
  primary: 'var(--brand-primary)',
  primaryHover: 'var(--brand-primary-hover)',
  primaryActive: 'var(--brand-primary-active)',
  surface: 'var(--brand-surface)',
  body: 'var(--brand-body)',
  border: 'var(--brand-border)',
  white: 'var(--brand-white)',
} as const

// Greys and the danger red stay literal: they are not themeable.
const GREY = {
  50: '#F8FAFC',
  200: '#E2E8F0',
  500: '#64748B',
  600: '#475569',
  700: '#334155',
} as const
const DANGER = { base: '#D52B1E', hover: '#94170E' } as const

// The hero's diagonal wedge: slants right as it descends, anchored to the
// right edge.
const WEDGE_CLIP = 'polygon(57% 0, 100% 0, 100% 100%, 74% 100%)'

// Type scale. Desktop sizes are the stijlgids values; the clamp()s only
// shrink the large steps on narrow viewports.
const TYPE = {
  display: 'clamp(2.25rem, 6vw, 4rem)',
  h1: 'clamp(1.75rem, 4vw, 2.25rem)',
  h2: 'clamp(1.5rem, 3vw, 2rem)',
  h3: '1.375rem',
} as const

// Hover and active states skip disabled buttons and disabled links.
const ENABLED = ':not(:disabled):not([aria-disabled="true"])'

export default {
  content: [
    './components/**/*.{vue,ts}',
    './composables/**/*.ts',
    './layouts/**/*.vue',
    './pages/**/*.vue',
    './app.vue',
  ],
  theme: {
    extend: {
      fontFamily: {
        sans: ['"Fira Sans"', 'Arial', 'Verdana', 'sans-serif'],
        mono: ['"Geist Mono"', 'ui-monospace', 'SF Mono', 'Menlo', 'monospace'],
      },
      backgroundImage: {
        'brand-wedge': 'var(--brand-wedge-gradient)',
      },
      fontSize: {
        display: [TYPE.display, { lineHeight: '1.1', letterSpacing: '-0.015em', fontWeight: '700' }],
        h1: [TYPE.h1, { lineHeight: '1.25', fontWeight: '700' }],
        h2: [TYPE.h2, { lineHeight: '1.25', fontWeight: '700' }],
        h3: [TYPE.h3, { lineHeight: '1.25', fontWeight: '700' }],
        'body-lg': ['1.25rem', { lineHeight: '1.5' }],
        body: ['1.125rem', { lineHeight: '1.5' }],
      },
      borderRadius: {
        card: '4px',
        // A single corner where a panel meets another surface.
        hero: '20px',
        // The single-corner page cut on the footer.
        cut: '40px',
      },
      boxShadow: {
        'elev-1': '0 1px 2px rgba(10,39,80,0.06)',
        'elev-2': '0 2px 6px rgba(10,39,80,0.08)',
        'elev-3': '0 8px 24px rgba(10,39,80,0.12)',
      },
      maxWidth: {
        // Content container.
        page: '1180px',
        // Rijksoverheid masthead row.
        masthead: '1280px',
      },
      colors: {
        brand: brandColors,
        govnl: {
          wit: '#ffffff',
          zwart: '#000000',
          grijs: {
            50:  '#F8FAFC',
            100: '#F1F5F9',
            200: '#E2E8F0',
            300: '#CBD5E1',
            400: '#94A3B8',
            500: '#64748B',
            600: '#475569',
            700: '#334155',
            800: '#1E293B',
            900: '#0F172A',
          },
          lintblauw: {
            50:  '#DCE3EA',
            100: '#B4BFCD',
            200: '#7E92AB',
            300: '#4F7196',
            400: '#2A5587',
            500: '#154273',
            600: '#11355C',
            700: '#0D2845',
            800: '#0A2750',
            900: '#051426',
          },
          hemelblauw: {
            50:  '#E1F0FA',
            100: '#B7DAF1',
            200: '#8BC5E8',
            300: '#4FB0DD',
            400: '#28A0D5',
            500: '#007BC7',
            600: '#006AAE',
            700: '#005A91',
            800: '#004878',
          },
          mintgroen: {
            100: '#CFEAE0',
            300: '#75C8AA',
          },
          groen: {
            50:  '#E8F4ED',
            300: '#6DA88B',
            400: '#399064',
            500: '#1E7D45',
            700: '#0F5F30',
          },
          donkergroen: {
            500: '#275937',
            700: '#173821',
          },
          mosgroen: {
            500: '#8F9F2B',
          },
          bruin: {
            100: '#ECE0D0',
            300: '#C8AE7E',
            500: '#94710A',
            700: '#5C460A',
          },
          // This repo owns the palette (#156): robijnrood-500 is #CA005D.
          robijnrood: {
            50:  '#F6DDE7',
            100: '#EFBBCF',
            500: '#CA005D',
            600: '#A30049',
            700: '#7D0038',
          },
          rood: {
            50:  '#FBEAE8',
            100: '#F5CCC9',
            200: '#EDADA9',
            300: '#E58379',
            400: '#DC6059',
            500: '#D52B1E',
            600: '#B82217',
            700: '#94170E',
          },
          oranje: {
            50:  '#FEF1E6',
            100: '#FDE3CD',
            200: '#FAC79B',
            300: '#F2A45C',
            400: '#F08030',
            500: '#EC6608',
            600: '#D55A06',
            700: '#B84D04',
          },
          geel: {
            50:  '#FEFCE7',
            100: '#FCEFAB',
            200: '#FAE472',
            300: '#F8D43D',
            400: '#F7CD28',
            500: '#F9E11E',
            600: '#DCB908',
            700: '#B89405',
          },
          roze: {
            50:  '#FCF0F5',
            100: '#F6DDE7',
            300: '#E289B1',
            500: '#C71E70',
            700: '#8B0F4D',
          },
          paars: {
            300: '#99AAFF',
            500: '#9747FF',
          },
        },
      },
    },
  },
  plugins: [
    // Component classes, prefixed brand- like the colours. At each heading
    // level the size, weight, colour and leading travel together, so a page
    // cannot get the type scale wrong. They sit in the components layer, so a
    // utility at the call site still wins.
    plugin(({ addComponents }) => {
      const heading = {
        color: B.primary,
        fontWeight: '700',
        lineHeight: '1.25',
        margin: '0',
        textWrap: 'balance',
      }

      addComponents({
        '.brand-display': { ...heading, fontSize: TYPE.display, lineHeight: '1.1', letterSpacing: '-0.015em' },
        '.brand-h1': { ...heading, fontSize: TYPE.h1 },
        '.brand-h2': { ...heading, fontSize: TYPE.h2 },
        '.brand-h3': { ...heading, fontSize: TYPE.h3 },

        // 20/150, the stijlgids "body large".
        '.brand-lead': { fontSize: '1.25rem', lineHeight: '1.5', color: B.body, margin: '0' },
        // 16/160 running text.
        '.brand-prose': { fontSize: '1rem', lineHeight: '1.6', color: B.body, margin: '0' },

        // Inline text link. The CTA colour is for primary actions, so links
        // take the primary colour.
        '.brand-link': {
          color: B.primary,
          textDecoration: 'underline',
          textUnderlineOffset: '3px',
          textDecorationThickness: '1px',
          '&:hover': { color: B.primaryHover, textDecorationThickness: '2px' },
          '&:active': { color: B.primaryActive },
        },

        // Buttons. GovnlButton composes these, and a plain
        // <button type="submit"> in a form can use them directly.
        '.brand-btn': {
          display: 'inline-flex',
          alignItems: 'center',
          justifyContent: 'center',
          gap: '0.5rem',
          padding: '0.625rem 1.25rem',
          borderRadius: '4px',
          border: '2px solid transparent',
          fontFamily: 'inherit',
          fontSize: '1rem',
          fontWeight: '600',
          lineHeight: '1.25',
          whiteSpace: 'nowrap',
          textDecoration: 'none',
          cursor: 'pointer',
          transition: 'background-color 150ms ease-out, border-color 150ms ease-out, color 150ms ease-out',
          '&:disabled, &[aria-disabled="true"]': { opacity: '0.5', cursor: 'not-allowed' },
        },
        '.brand-btn-sm': { padding: '0.375rem 0.875rem', gap: '0.375rem', fontSize: '0.875rem' },
        '.brand-btn-lg': { padding: '0.875rem 1.75rem', gap: '0.625rem', fontSize: '1.125rem' },

        '.brand-btn-primary': {
          backgroundColor: B.cta,
          borderColor: B.cta,
          color: B.white,
          [`&:hover${ENABLED}, &:focus-visible${ENABLED}`]: { backgroundColor: B.ctaHover, borderColor: B.ctaHover },
          [`&:active${ENABLED}`]: { backgroundColor: B.ctaActive, borderColor: B.ctaActive },
        },
        '.brand-btn-navy': {
          backgroundColor: B.primary,
          borderColor: B.primary,
          color: B.white,
          [`&:hover${ENABLED}, &:focus-visible${ENABLED}`]: { backgroundColor: B.primaryHover, borderColor: B.primaryHover },
          [`&:active${ENABLED}`]: { backgroundColor: B.primaryActive, borderColor: B.primaryActive },
        },
        '.brand-btn-secondary': {
          backgroundColor: B.white,
          borderColor: B.primary,
          color: B.primary,
          [`&:hover${ENABLED}, &:focus-visible${ENABLED}`]: { backgroundColor: B.surface },
          [`&:active${ENABLED}`]: { backgroundColor: GREY[200] },
        },
        '.brand-btn-ghost': {
          backgroundColor: 'transparent',
          borderColor: B.white,
          color: B.white,
          [`&:hover${ENABLED}, &:focus-visible${ENABLED}`]: { backgroundColor: 'rgba(255,255,255,0.12)' },
        },
        '.brand-btn-danger': {
          backgroundColor: DANGER.base,
          borderColor: DANGER.base,
          color: B.white,
          [`&:hover${ENABLED}, &:focus-visible${ENABLED}`]: { backgroundColor: DANGER.hover, borderColor: DANGER.hover },
        },
        '.brand-btn-link': {
          padding: '0',
          gap: '0.375rem',
          backgroundColor: 'transparent',
          borderColor: 'transparent',
          color: B.primary,
          textDecoration: 'underline',
          textUnderlineOffset: '3px',
          [`&:hover${ENABLED}, &:focus-visible${ENABLED}`]: { color: B.primaryHover },
        },

        // Flat card: a 1px border all round, never an accent edge.
        '.brand-card': {
          backgroundColor: B.white,
          border: `1px solid ${B.border}`,
          borderRadius: '4px',
        },
        '.brand-card-interactive': {
          transition: 'background-color 150ms ease-out, border-color 150ms ease-out',
          '&:hover': { backgroundColor: B.surface, borderColor: GREY[500] },
          '&:active': { backgroundColor: GREY[200], borderColor: GREY[700] },
        },

        // Form controls.
        '.brand-label': {
          display: 'block',
          fontSize: '1rem',
          fontWeight: '600',
          color: B.primary,
          marginBottom: '0.375rem',
        },
        '.brand-input': {
          width: '100%',
          minHeight: '2.75rem',
          padding: '0.5rem 0.875rem',
          backgroundColor: B.white,
          color: B.body,
          // grijs-500, not the border token: a control's edge needs 3:1
          // against white (WCAG 1.4.11), and the decorative border is 1.48:1.
          border: `1px solid ${GREY[500]}`,
          borderRadius: '4px',
          fontFamily: 'inherit',
          fontSize: '1rem',
          lineHeight: '1.5',
          '&::placeholder': { color: GREY[600] },
          '&:hover': { borderColor: GREY[700] },
          '&:disabled': { backgroundColor: GREY[50], color: GREY[600], cursor: 'not-allowed' },
        },
        '.brand-help': {
          margin: '0.375rem 0 0',
          fontSize: '0.875rem',
          lineHeight: '1.5',
          color: GREY[600],
        },

        // Key/value table on detail pages: hairline rows, no zebra.
        '.brand-table': {
          width: '100%',
          borderCollapse: 'collapse',
          fontSize: '1rem',
          '& tr': { borderTop: `1px solid ${B.border}` },
          '& td, & th': { padding: '0.6875rem 0', textAlign: 'left', verticalAlign: 'top' },
          '& th': { color: GREY[600], fontWeight: '400' },
          '& td': { color: B.body },
        },

        // The hero's diagonal wedge. Decorative, so always aria-hidden.
        '.brand-wedge': {
          position: 'absolute',
          inset: '0',
          backgroundImage: 'var(--brand-wedge-gradient)',
          clipPath: WEDGE_CLIP,
          pointerEvents: 'none',
        },
      })
    }),
  ],
} satisfies Config
