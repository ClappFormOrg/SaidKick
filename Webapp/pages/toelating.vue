<script setup lang="ts">
import {
  IconShieldCheck, IconLock, IconEye, IconScale, IconApps, IconCloud,
  IconDownload, IconFileCode, IconExternalLink,
} from '~/lib/icons'

useHead({ title: 'Toelatingseisen' })
import type { Component } from 'vue'

interface CriterionDetail {
  key: string
  label: string
  summary: string
  legalBasis: string
  legalRef: string
  required: string[]
  moreInfoUrl?: string
  moreInfoLabel?: string
}

const CRITERIA_DETAIL: CriterionDetail[] = [
  {
    key: 'dpia',
    label: 'DPIA',
    summary: 'Data Protection Impact Assessment beoordeeld en gepubliceerd.',
    legalBasis: 'AVG artikel 35 / GDPR',
    legalRef: 'Verordening (EU) 2016/679, art. 35; Uitvoeringswet AVG',
    required: [
      'Gepubliceerde DPIA conform de richtlijnen van de AP (NOREA/PIA-methodiek).',
      'Naam en contactgegevens van de Functionaris Gegevensbescherming (FG).',
      'Verwerkersovereenkomst (indien van toepassing).',
      'Beschrijving van doeleinden, categorieën persoonsgegevens en bewaartermijnen.',
    ],
    moreInfoUrl: 'https://www.autoriteitpersoonsgegevens.nl/themas/beveiliging/data-protection-impact-assessment-dpia',
    moreInfoLabel: 'AP — DPIA-richtlijnen',
  },
  {
    key: 'bio',
    label: 'BIO-conform',
    summary: 'Voldoet aan de Baseline Informatiebeveiliging Overheid.',
    legalBasis: 'Baseline Informatiebeveiliging Overheid (BIO 2.0)',
    legalRef: 'BIO 2.0 (vastgesteld door de Rijksoverheid; ISO 27001/27002-basis)',
    required: [
      'Ingevuld BIO-zelfassessment of gelijkwaardig certificeringsbewijs (ISO 27001 of SOC 2 Type II).',
      'Penetratietest-rapport door een geaccrediteerde partij (OSCP, CREST of TIBER-equivalent), niet ouder dan 12 maanden.',
      'Risicobehandeling (risicobeoordeling met acceptatiecriteria).',
      'Incidentrespons- en patch-management beleid.',
    ],
    moreInfoUrl: 'https://www.digitaleoverheid.nl/overzicht-van-alle-onderwerpen/cybersecurity/bio-en-ensia/',
    moreInfoLabel: 'Digitale Overheid — BIO 2.0',
  },
  {
    key: 'a11y',
    label: 'Toegankelijk',
    summary: 'WCAG 2.2 niveau AA, getoetst door een onafhankelijk bureau.',
    legalBasis: 'Tijdelijk besluit digitale toegankelijkheid overheid / EN 301 549',
    legalRef: 'Besluit van 14 september 2018, Stb. 2018, 312; EN 301 549 v3.2.1',
    required: [
      'WCAG 2.2 Level AA-audit door een onafhankelijk, geaccrediteerd toetsbureau.',
      'Gepubliceerde toegankelijkheidsverklaring conform het model van DigiToegankelijk.nl.',
      'Correctief plan voor eventuele resterende bevindingen (met termijnen).',
    ],
    moreInfoUrl: 'https://www.digitoegankelijk.nl',
    moreInfoLabel: 'DigiToegankelijk.nl',
  },
  {
    key: 'risk',
    label: 'AI-risicocategorie',
    summary: 'Classificatie volgens EU AI Act, met onderbouwing.',
    legalBasis: 'EU AI Act — Verordening (EU) 2024/1689',
    legalRef: 'Verordening (EU) 2024/1689 van het Europees Parlement en de Raad',
    required: [
      'Schriftelijke risicoclassificatie (minimaal, beperkt, hoog risico of verboden) met onderbouwing.',
      'Voor hoog-risico AI: conformiteitsbeoordelingsdossier inclusief technische documentatie (Bijlage IV AI Act).',
      'Voor hoog-risico AI: registratie in de EU AI-databank vóór ingebruikname.',
      'Beschrijving van menselijk toezicht (human-in-the-loop) en veiligheidsvoorzieningen.',
    ],
    moreInfoUrl: 'https://digital-strategy.ec.europa.eu/nl/policies/regulatory-framework-ai',
    moreInfoLabel: 'EC — AI Act regelgevingskader',
  },
  {
    key: 'oss',
    label: 'Open-source',
    summary: 'Broncode beschikbaar onder een goedgekeurde licentie.',
    legalBasis: 'OSPO-beleid Rijksoverheid / "Open tenzij"-principe',
    legalRef: 'Instructie Rijksdienst bij aanschaf ICT-diensten; publicatiebeleidsbrief BZK 2020',
    required: [
      'Broncode openbaar beschikbaar op een publieke repository (bij voorkeur GitHub).',
      'Licentie goedgekeurd door de Open Source Initiative (bijv. EUPL-1.2, Apache-2.0, MIT, GPL-3.0).',
      'REUSE-compliant of gelijkwaardige SBOM (software bill of materials).',
      'Actief onderhouden: minimaal één commit in de laatste 6 maanden.',
    ],
    moreInfoUrl: 'https://opensource.pleio.nl',
    moreInfoLabel: 'opensource.pleio.nl',
  },
  {
    key: 'local',
    label: 'Lokaal te draaien',
    summary: 'Inzetbaar op eigen cloud, on-premise of via SURF.',
    legalBasis: 'Soevereiniteits- en datalokaliteitsvereisten Rijksoverheid',
    legalRef: 'Rijkscloudbeleid (Kamerstuk 26 643, nr. 1014); SURF-overeenkomsten',
    required: [
      'Helm chart of gelijkwaardige Kubernetes-manifest-set, gedocumenteerd en geverifieerd.',
      'Aantoonbaar te draaien in een SURF Research Cloud-, NLCloud-, of on-premise Kubernetes-omgeving.',
      'Geen verplichte afhankelijkheden van niet-Europese public-cloud diensten zonder gelijkwaardig alternatief.',
      'Deployment-documentatie inclusief systeemvereisten, schaalbaarheidsgrenzen en back-upstrategie.',
    ],
    moreInfoUrl: 'https://www.surf.nl/diensten',
    moreInfoLabel: 'SURF — diensten',
  },
]

const CRITERION_ICONS: Record<string, Component> = {
  dpia:  IconShieldCheck,
  bio:   IconLock,
  a11y:  IconEye,
  risk:  IconScale,
  oss:   IconApps,
  local: IconCloud,
}

const PROCEDURE: { title: string; body: string; duration: string | null }[] = [
  {
    title: 'Aanmelding',
    body: 'De aanbieder dient de applicatie in via het dashboard. Daarvoor is een organisatieaccount vereist. Bij indiening worden het manifest, de DPIA, het BIO-assessment en eventuele overige documenten geüpload.',
    duration: null,
  },
  {
    title: 'Automatische validatie',
    body: 'Direct na indiening controleert het platform het manifest op volledigheid en formaat (Stage 1). Ontbrekende velden of foutieve sleutelwaarden worden direct teruggekoppeld. Dit duurt doorgaans minder dan een minuut.',
    duration: '< 1 minuut',
  },
  {
    title: 'Inhoudelijke beoordeling',
    body: 'Een auditcommissie van onafhankelijke experts toetst de ingediende bewijsdocumenten aan de zes criteria. De commissie kan aanvullende vragen stellen; antwoorden dienen binnen vijf werkdagen te worden aangeleverd.',
    duration: '4–6 weken',
  },
  {
    title: 'Terugkoppeling',
    body: 'De aanbieder ontvangt een gedetailleerd beoordelingsrapport. Bij een gedeeltelijk negatief advies kan aanvullende informatie worden aangeleverd voor een herindiening. Een volledig negatief oordeel kan worden heroverwogen na aantoonbare herstelmaatregelen.',
    duration: null,
  },
  {
    title: 'Publicatie',
    body: 'Goedgekeurde applicaties worden gepubliceerd in de catalogus. Het toelatingsoordeel — inclusief de beoordeelde criteria — is openbaar en wordt bijgehouden in het auditlogboek voor de wettelijke bewaartermijn van zeven jaar.',
    duration: null,
  },
]

const FAQ: { q: string; a: string }[] = [
  {
    q: 'Gelden de toelatingseisen ook voor interne overheidsapplicaties?',
    a: 'Ja. De eisen zijn gelijk voor alle aanbieders, ongeacht of het om een overheidsorganisatie of een marktpartij gaat. Overheidsorganisaties kunnen voor de BIO-toets gebruikmaken van hun bestaande ENSIA-verantwoording.',
  },
  {
    q: 'Hoe lang is een toelatingsoordeel geldig?',
    a: 'Een oordeel is geldig voor twee jaar. Bij een grote release (major versiewijziging) of significante wijziging in verwerkingsprocessen of risicocategorie dient een hertoetsing te worden aangevraagd.',
  },
  {
    q: 'Wat als mijn applicatie \'hoog risico\' is volgens de EU AI Act?',
    a: 'Hoog-risico AI-systemen zijn welkom in de catalogus mits volledig gecompliant met de EU AI Act. Dit betekent onder meer: een conformiteitsbeoordelingsdossier, registratie in de EU AI-databank en adequate toezichtsmaatregelen. De auditcommissie beschikt over expertise op dit gebied.',
  },
  {
    q: 'Kan ik een al gepubliceerde DPIA hergebruiken?',
    a: 'Ja, mits de DPIA de huidige versie van de applicatie dekt en niet ouder is dan drie jaar. Oudere of gedeeltelijk dekkende DPIA\'s worden ter beoordeling teruggekoppeld met een verzoek tot actualisatie.',
  },
]
</script>

<template>
  <div>
    <div class="border-b border-brand-border bg-brand-white">
      <div class="mx-auto max-w-page px-6 py-6">
        <UiBreadcrumb
          class="mb-3"
          :items="[{ label: 'Home', to: '/' }, { label: 'Toelatingseisen' }]"
        />
        <h1 id="toelating-heading" class="brand-h1">Toelatingseisen</h1>
        <p class="brand-lead mt-3 max-w-[40rem]">
          Iedere applicatie in de AI-Appstore is getoetst op zes criteria. Alleen applicaties die aan alle zes voldoen worden toegelaten tot de catalogus.
        </p>
      </div>
    </div>

    <section aria-label="Inleiding beoordelingskader" class="mx-auto max-w-page px-6 pt-10">
      <div class="max-w-[51rem] rounded-card bg-brand-secondary px-7 py-6">
        <p class="brand-prose m-0 leading-[1.7]">
          Het beoordelingskader is opgesteld door de deelnemende provincies en publieke organisaties in samenwerking met juridische en technische experts. Het kader sluit aan op bestaande rijksstandaarden en Europese regelgeving — waaronder de AVG, BIO 2.0 en de EU AI Act — en wordt jaarlijks herzien. Aanbieders die voldoen aan alle zes criteria ontvangen een toelatingsoordeel dat openbaar wordt gepubliceerd in het auditlogboek.
        </p>
      </div>
    </section>

    <section aria-labelledby="criteria-heading" class="mx-auto max-w-page px-6 pb-16 pt-10">
      <h2 id="criteria-heading" class="brand-h2 mb-6">De zes criteria</h2>

      <ol class="m-0 flex list-none flex-col gap-6 p-0">
        <li
          v-for="(crit, i) in CRITERIA_DETAIL"
          :key="crit.key"
          class="brand-card px-8 py-7"
        >
          <div class="mb-4 flex items-center gap-4">
            <span class="flex h-11 w-11 shrink-0 items-center justify-center rounded-card border border-brand-border bg-brand-surface text-brand-primary">
              <component :is="CRITERION_ICONS[crit.key]" :size="22" :stroke="2" aria-hidden="true" />
            </span>
            <span>
              <span class="block text-sm text-govnl-grijs-600">Criterium {{ i + 1 }} van {{ CRITERIA_DETAIL.length }}</span>
              <h3 class="brand-h3 m-0">{{ crit.label }}</h3>
            </span>
          </div>

          <p class="brand-prose mb-5 mt-0 leading-[1.65]">{{ crit.summary }}</p>

          <div class="flex flex-col gap-6 sm:flex-row">
            <div class="flex-1">
              <h4 class="mb-2 mt-0 font-bold text-brand-primary">Vereist bewijs</h4>
              <ul class="brand-prose m-0 flex list-disc flex-col gap-1.5 pl-5">
                <li v-for="item in crit.required" :key="item">{{ item }}</li>
              </ul>
            </div>

            <div class="shrink-0 sm:w-64">
              <h4 class="mb-2 mt-0 font-bold text-brand-primary">Rechtsgrondslag</h4>
              <div class="rounded-card border border-brand-border bg-brand-surface px-4 py-3">
                <p class="m-0 text-sm font-semibold text-brand-primary">{{ crit.legalBasis }}</p>
                <p class="m-0 mt-0.5 text-sm leading-[1.45] text-govnl-grijs-700">{{ crit.legalRef }}</p>
              </div>
              <a
                v-if="crit.moreInfoUrl"
                :href="crit.moreInfoUrl"
                target="_blank"
                rel="noopener noreferrer"
                class="brand-link mt-2.5 inline-flex items-center gap-1.5 text-sm font-semibold"
              >
                <IconExternalLink size="14" stroke="2" aria-hidden="true" />
                {{ crit.moreInfoLabel }}<span class="sr-only"> (opent in nieuw tabblad)</span>
              </a>
            </div>
          </div>
        </li>
      </ol>
    </section>

    <section aria-label="Voorbeeld manifest downloaden" class="mx-auto max-w-page px-6 pb-12">
      <div class="flex flex-wrap items-center justify-between gap-6 rounded-card bg-brand-secondary px-8 py-7">
        <div class="flex items-start gap-4">
          <span class="flex h-11 w-11 shrink-0 items-center justify-center rounded-card bg-brand-primary text-brand-white">
            <IconFileCode size="22" stroke="2" aria-hidden="true" />
          </span>
          <div>
            <h2 class="brand-h3 mb-1.5 mt-0">
              Voorbeeld <code class="font-mono">appstore.yaml</code>
            </h2>
            <p class="brand-prose m-0 max-w-[33rem]">
              Download het voorbeeldmanifest, vul alle verplichte velden in en plaats het als <code class="font-mono text-sm">appstore.yaml</code> in de root van uw GitHub-repository. Het manifest bevat ook de DPIA-verwijzing en AI-risicocategorieinstelling.
            </p>
          </div>
        </div>
        <!-- Navy, not primary: "App aanmelden" below is the page's main action. -->
        <GovnlButton
          variant="navy"
          href="/appstore.example.yaml"
          download="appstore.example.yaml"
          :icon="IconDownload"
        >
          Download manifest
        </GovnlButton>
      </div>
    </section>

    <section aria-labelledby="procedure-heading" class="rounded-tl-cut bg-brand-surface py-14">
      <div class="mx-auto max-w-page px-6">
        <h2 id="procedure-heading" class="brand-h2 mb-8">Toelatingsprocedure</h2>

        <ol class="m-0 flex max-w-[45rem] list-none flex-col p-0">
          <li
            v-for="(step, i) in PROCEDURE"
            :key="step.title"
            class="flex items-stretch gap-5"
          >
            <div class="flex flex-col items-center" aria-hidden="true">
              <span class="z-10 flex h-8 w-8 shrink-0 items-center justify-center rounded-full bg-brand-primary text-sm font-bold text-brand-white">{{ i + 1 }}</span>
              <span v-if="i < PROCEDURE.length - 1" class="mt-1 w-px flex-1 bg-brand-border" />
            </div>

            <div class="flex-1 pb-8">
              <div class="mb-1.5 flex flex-wrap items-center gap-3">
                <h3 class="m-0 font-bold text-brand-primary">{{ i + 1 }}. {{ step.title }}</h3>
                <UiTag v-if="step.duration">{{ step.duration }}</UiTag>
              </div>
              <p class="brand-prose m-0 leading-[1.65]">{{ step.body }}</p>
            </div>
          </li>
        </ol>
      </div>
    </section>

    <section aria-labelledby="faq-heading" class="mx-auto max-w-page px-6 py-12">
      <h2 id="faq-heading" class="brand-h2 mb-6">Veelgestelde vragen</h2>
      <dl class="m-0 flex max-w-[45rem] flex-col gap-4">
        <div v-for="faq in FAQ" :key="faq.q" class="brand-card px-6 py-5">
          <dt class="mb-2 font-bold text-brand-primary">{{ faq.q }}</dt>
          <dd class="brand-prose m-0 leading-[1.65]">{{ faq.a }}</dd>
        </div>
      </dl>
    </section>

    <section aria-label="Aan de slag" class="mx-auto max-w-page px-6 pb-16">
      <div class="flex flex-wrap gap-4">
        <GovnlButton variant="primary" to="/dashboard/apps/new">App aanmelden</GovnlButton>
        <GovnlButton variant="secondary" to="/catalogus">Bekijk catalogus</GovnlButton>
      </div>
    </section>
  </div>
</template>
