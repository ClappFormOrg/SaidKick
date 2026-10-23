import type { App, Category, Criterion } from '~/types'

// ─── API response shapes (internal) ──────────────────────────────────────────

export interface AppCardAPI {
  id: string
  slug: string
  vendor: string
  state: string
  github_repo: string
  display_name?: string
  categories?: string[]
  risk_class?: string
  license?: string
  // Helm chart version of the published version (catalog.go AppCard).
  chart_version?: string
  last_published_at?: string
  auto_validation_state?: 'passed' | 'failed' | 'pending'
  audit_approved?: boolean
  // Whether the caller's own organisation runs this app. False for anonymous.
  deployed_in_my_org?: boolean
}

export interface AppDetailAPI extends AppCardAPI {
  owning_organization_id: string
  owning_user_id: string
  github_installation_id: number
  github_default_branch: string
  repo_link_state: string
  created_at: string
  updated_at: string
  versie?: string
  installs?: number
  auto_validation_state?: 'passed' | 'failed' | 'pending'
  audit_approved?: boolean
  current_published_version_id?: string
  // Live deployments in the caller's own organisation. [] for anonymous.
  my_deployments?: Array<{ deployment_id: string; availability: Availability; url?: string }>
  manifest?: {
    metadata: { name: string; display_name: string; vendor: string; contact: string; license: string }
    spec: {
      categories?: string[]
      ai?: { risk_class?: string; base_model?: string; data_residency?: string }
      demo?: { description?: string; screenshots?: string[] }
      audit?: { dpia?: string; security_scan?: string; ai_risk?: string; accessibility?: string; model_card?: string }
      interfaces?: Array<{ type: string; openapi?: string }>
    }
  }
}

export interface AuditSubmissionDetail {
  id: string
  auto_validation_state: 'passed' | 'failed' | 'pending'
  auto_validation_errors?: string[]
  form_version: string
  evidence_files: Array<{
    logical_name: string
    repo_path: string
    content_sha256: string
    size_bytes: number
    content_type: string
  }>
  created_at: string
  app: { slug: string; name: string; owning_organization_id: string }
  version: { git_ref: string; git_sha: string; state: string }
}

export interface AuditQueueItem {
  id: string
  auto_validation_state: 'passed' | 'failed' | 'pending'
  form_version: string
  created_at: string
  app: { slug: string; name: string; risk_class: string }
  version: { git_ref: string; git_sha: string }
}

export interface OrgMember {
  id: string
  email: string
  display_name: string
  roles: string[]
  last_login_at?: string
  created_at: string
}

// ─── Mapping helpers ──────────────────────────────────────────────────────────

export function stateToStatus(state: string): string {
  switch (state) {
    case 'published':     return 'Productie'
    case 'submitted':     return 'Pilot'
    case 'needs_changes': return 'Testfase'
    case 'draft':         return 'Concept'
    case 'archived':      return 'Uitfaserend'
    default:              return 'Concept'
  }
}

export function riskClassToLabel(rc: string): string {
  switch (rc) {
    case 'minimal': return 'Minimaal risico'
    case 'limited': return 'Beperkt risico'
    case 'high':    return 'Hoog risico'
    default:        return rc || 'Onbekend'
  }
}

export function mapCard(app: AppCardAPI): App {
  const cats = app.categories ?? []
  return {
    id:                app.id,
    slug:              app.slug,
    name:              app.display_name || app.slug,
    tagline:           '',
    description:       '',
    longDescription:   [],
    category:          cats[0] ?? '',
    status:            stateToStatus(app.state),
    tags:              cats,
    eigenaar:          app.vendor,
    medeontwikkelaars: [],
    versie:            app.chart_version ?? '',
    bijgewerkt:        app.last_published_at ?? '',
    licentie:          app.license ?? '',
    repo:              app.github_repo,
    scores:            deriveScores(app.auto_validation_state, app.audit_approved),
    riskCategory:      riskClassToLabel(app.risk_class ?? ''),
    installs:          0,
  }
}

type ScoreValue = 'pass' | 'partial' | 'fail' | 'pending'

export function deriveScores(state?: string, approved?: boolean): Record<string, ScoreValue> {
  if (approved) return { dpia: 'pass', bio: 'pass', a11y: 'pass', risk: 'pass', oss: 'pass', local: 'pass' }
  if (state === 'passed') return { bio: 'pass', risk: 'pass', dpia: 'pending', a11y: 'pending', oss: 'pending', local: 'pending' }
  if (state === 'failed') return { bio: 'fail', risk: 'fail', dpia: 'pending', a11y: 'pending', oss: 'pending', local: 'pending' }
  return {}
}

const AUDIT_DOC_LABELS: Record<string, string> = {
  dpia:          'DPIA-rapport',
  security_scan: 'Beveiligingsscan',
  ai_risk:       'AI-risicobeoordeling',
  accessibility: 'Toegankelijkheidsrapport (WCAG)',
  model_card:    'Modelkaart',
}

export function buildScreenshots(repo: string, spec: NonNullable<AppDetailAPI['manifest']>['spec']): string[] {
  const rawBase = `https://raw.githubusercontent.com/${repo}/main/`
  return (spec?.demo?.screenshots ?? []).map(p => rawBase + p.replace(/^\.\//, ''))
}

export function buildDocs(repo: string, spec: NonNullable<AppDetailAPI['manifest']>['spec']): { label: string; url: string }[] {
  const base = `https://github.com/${repo}/blob/main/`
  const toUrl = (path: string) => base + path.replace(/^\.\//, '')
  const docs: { label: string; url: string }[] = []

  const audit = spec?.audit
  if (audit) {
    for (const [key, label] of Object.entries(AUDIT_DOC_LABELS)) {
      const path = audit[key as keyof typeof audit]
      if (path) docs.push({ label, url: toUrl(path) })
    }
  }

  for (const iface of spec?.interfaces ?? []) {
    if (iface.type === 'api' && iface.openapi) {
      docs.push({ label: 'API-documentatie', url: toUrl(iface.openapi) })
    }
  }

  return docs
}

// Admission criteria whose evidence the manifest's spec.audit block names
// directly. Criteria without an entry here have no linkable evidence.
const CRITERION_EVIDENCE: Record<string, keyof NonNullable<NonNullable<AppDetailAPI['manifest']>['spec']['audit']>> = {
  dpia: 'dpia',
  a11y: 'accessibility',
  risk: 'ai_risk',
}

export function buildEvidence(repo: string, spec: NonNullable<AppDetailAPI['manifest']>['spec']): Record<string, string> {
  const base = `https://github.com/${repo}/blob/main/`
  const evidence: Record<string, string> = {}
  for (const [criterion, auditKey] of Object.entries(CRITERION_EVIDENCE)) {
    const path = spec?.audit?.[auditKey]
    if (path) evidence[criterion] = base + path.replace(/^\.\//, '')
  }
  return evidence
}

export function mapDetail(detail: AppDetailAPI): App {
  const base = mapCard(detail)
  const manifest = detail.manifest
  const versie           = detail.chart_version || detail.versie || base.versie
  const installs         = detail.installs ?? base.installs
  const scores           = deriveScores(detail.auto_validation_state, detail.audit_approved)
  const currentVersionId = detail.current_published_version_id
  const myDeployments    = (detail.my_deployments ?? []).map(d => ({
    deploymentId: d.deployment_id,
    availability: d.availability,
    url:          d.url,
  }))
  if (!manifest) return { ...base, versie, installs, scores, currentVersionId, myDeployments }
  const spec = manifest.spec
  const cats = spec.categories ?? base.tags
  const docs           = buildDocs(base.repo, spec)
  const screenshots    = buildScreenshots(base.repo, spec)
  const rawBase        = `https://raw.githubusercontent.com/${base.repo}/main/`
  const descriptionUrl = spec?.demo?.description
    ? rawBase + spec.demo.description.replace(/^\.\//, '')
    : undefined
  return {
    ...base,
    name:         manifest.metadata.display_name || base.name,
    eigenaar:     manifest.metadata.vendor || base.eigenaar,
    licentie:     manifest.metadata.license || base.licentie,
    category:     cats[0] ?? base.category,
    tags:         cats,
    riskCategory: riskClassToLabel(spec.ai?.risk_class ?? ''),
    bijgewerkt:   detail.updated_at
      ? new Date(detail.updated_at).toLocaleDateString('nl-NL', { day: 'numeric', month: 'long', year: 'numeric' })
      : base.bijgewerkt,
    descriptionUrl,
    versie,
    installs,
    scores,
    currentVersionId,
    docs,
    evidence: buildEvidence(base.repo, spec),
    screenshots,
    myDeployments,
    vendorContact: manifest.metadata.contact || undefined,
  }
}

// ─── Static catalogue configuration ──────────────────────────────────────────

export const CATEGORIES: Category[] = [
  { slug: 'agent',       title: 'Agents',         icon: 'robot' },
  { slug: 'chatbot',     title: 'Chatbots',       icon: 'message-circle' },
  { slug: 'rag',         title: 'RAG & zoeken',   icon: 'database' },
  { slug: 'docs',        title: 'Documenten',     icon: 'file-text' },
  { slug: 'analyse',     title: 'Data-analyse',   icon: 'chart-line' },
  { slug: 'infrastruct', title: 'Infrastructuur', icon: 'cloud' },
]

export const CRITERIA: Criterion[] = [
  { key: 'dpia',  label: 'DPIA',               desc: 'Data Protection Impact Assessment beoordeeld en gepubliceerd.' },
  { key: 'bio',   label: 'BIO-conform',        desc: 'Voldoet aan de Baseline Informatiebeveiliging Overheid.' },
  { key: 'a11y',  label: 'Toegankelijk',       desc: 'WCAG 2.2 niveau AA, getoetst door een onafhankelijk bureau.' },
  { key: 'risk',  label: 'AI-risicocategorie', desc: 'Classificatie volgens EU AI Act, met onderbouwing.' },
  { key: 'oss',   label: 'Open-source',        desc: 'Broncode beschikbaar onder een goedgekeurde licentie.' },
  { key: 'local', label: 'Lokaal te draaien',  desc: 'Inzetbaar op eigen cloud, on-premise of via SURF.' },
]

// The dot colour UiStatusPill shows next to a status label. The label always
// names the status, so the colour is never the only signal; it is decorative.
export function statusDotColor(status: string): string {
  switch (status) {
    case 'Productie':   return 'var(--govnl-color-groen-500)'
    case 'Pilot':       return 'var(--govnl-color-hemelblauw-500)'
    case 'Testfase':    return 'var(--govnl-color-oranje-500)'
    case 'Concept':     return 'var(--govnl-color-grijs-500)'
    case 'Uitfaserend': return 'var(--govnl-color-rood-500)'
    // Deployed-app availability labels (availabilityLabel), for the launcher.
    case 'Beschikbaar':         return 'var(--govnl-color-groen-500)'
    case 'Verminderde werking': return 'var(--govnl-color-oranje-500)'
    case 'Wordt voorbereid':    return 'var(--govnl-color-hemelblauw-500)'
    case 'Niet beschikbaar':    return 'var(--govnl-color-rood-500)'
    default:                    return 'var(--govnl-color-grijs-500)'
  }
}

// ─── Auto-validation badge helpers ───────────────────────────────────────────

export function autoValColor(state: string): { bg: string; fg: string } {
  switch (state) {
    case 'passed': return { bg: 'bg-govnl-groen-50',  fg: 'text-govnl-groen-700' }
    case 'failed': return { bg: 'bg-govnl-rood-50',   fg: 'text-govnl-rood-700' }
    default:       return { bg: 'bg-govnl-grijs-100', fg: 'text-govnl-grijs-600' }
  }
}

export function autoValLabel(state: string): string {
  switch (state) {
    case 'passed': return 'Geslaagd'
    case 'failed': return 'Mislukt'
    default:       return 'In behandeling'
  }
}

// ─── Risk badge helpers ───────────────────────────────────────────────────────

export function riskLabel(rc: string): string {
  switch (rc) {
    case 'minimal': return 'Minimaal'
    case 'limited': return 'Beperkt'
    case 'high':    return 'Hoog'
    default:        return rc || '—'
  }
}

export function riskColor(rc: string): { bg: string; fg: string } {
  switch (rc) {
    case 'high':    return { bg: 'bg-govnl-rood-50',   fg: 'text-govnl-rood-700' }
    case 'limited': return { bg: 'bg-govnl-oranje-50', fg: 'text-govnl-oranje-700' }
    default:        return { bg: 'bg-govnl-groen-50',  fg: 'text-govnl-groen-700' }
  }
}

export const SCORE: Record<string, { label: string; color: string }> = {
  pass:    { label: 'Voldoet',        color: 'var(--govnl-color-groen-500)' },
  partial: { label: 'Gedeeltelijk',   color: 'var(--govnl-color-oranje-500)' },
  fail:    { label: 'Voldoet niet',   color: 'var(--govnl-color-rood-500)' },
  pending: { label: 'In beoordeling', color: 'var(--govnl-color-grijs-500)' },
}

// ─── API composables ──────────────────────────────────────────────────────────

export function useApps() {
  const { api } = useApi()
  return useAsyncData('apps', async () => {
    const data = await api<{ items: AppCardAPI[]; total: number }>(
      '/api/v1/apps',
      { query: { page_size: 100 } },
    )
    return (data.items ?? []).map(mapCard)
  }, { default: (): App[] => [] })
}

export function useApp(slug: string) {
  const { api } = useApi()
  return useAsyncData(`app:${slug}`, () =>
    api<AppDetailAPI>(`/api/v1/apps/${slug}`).then(mapDetail),
  )
}

// ─── Deployment composable ────────────────────────────────────────────────────

interface DeploymentAPI {
  id: string
  app_id: string
  cluster_id: string
  namespace: string
  state: string
  last_state_change_at: string
}

export interface DeploymentList {
  items: DeploymentAPI[]
  total: number
}

export function useDeployments() {
  const { api } = useApi()
  return useAsyncData<DeploymentList>(
    'deployments',
    () => api<DeploymentList>('/api/v1/deployments'),
    { server: false, default: (): DeploymentList => ({ items: [], total: 0 }) },
  )
}

// ─── App launcher ("Mijn apps") ──────────────────────────────────────────────

// Coarse availability of a deployed app as GET /my-apps reports it, instead
// of the raw deployment state.
export type Availability = 'available' | 'degraded' | 'preparing' | 'unavailable'

// Who to contact about a deployed app. Every field may be absent.
export interface Maintainer {
  name?: string
  email?: string
  vendor_name?: string
  vendor_contact?: string
}

export interface MyAppCard {
  deployment_id: string
  app_slug: string
  display_name: string
  categories: string[]
  cluster_name?: string
  availability: Availability
  access: 'web' | 'none'
  // Present only when the app has a web interface and is up.
  url?: string
  maintainer: Maintainer
}

export interface MyAppList {
  items: MyAppCard[]
  total: number
}

// The live deployments of the signed-in user's own organisation. Not
// useMyApps, which lists the apps a developer submitted.
export function useMyDeployedApps() {
  const { api } = useApi()
  return useAsyncData<MyAppList>(
    'my-deployed-apps',
    () => api<MyAppList>('/api/v1/my-apps', { query: { page_size: 100 } }),
    { server: false, default: (): MyAppList => ({ items: [], total: 0 }) },
  )
}

const AVAILABILITY_LABELS: Record<Availability, string> = {
  available:   'Beschikbaar',
  degraded:    'Verminderde werking',
  preparing:   'Wordt voorbereid',
  unavailable: 'Niet beschikbaar',
}

// Dutch label for an availability value. An unknown value is shown as is
// rather than mislabelled.
export function availabilityLabel(a: string): string {
  return AVAILABILITY_LABELS[a as Availability] ?? a
}

// A link to ask about access to a deployed app: the deployer first, since
// they are in the same organisation, then the vendor contact. A vendor
// contact may be a helpdesk URL. Returns '' when nothing usable resolves, so
// the caller shows plain text instead of a dead link.
export function maintainerLink(m: Maintainer | undefined, appName: string): string {
  for (const contact of [m?.email, m?.vendor_contact]) {
    const to = contact?.trim()
    if (!to) continue
    if (/^https:\/\//i.test(to)) return to
    if (/^[^\s@/?#:]+@[^\s@/?#:]+\.[^\s@/?#:]+$/.test(to)) {
      return `mailto:${to}?subject=${encodeURIComponent(`Toegang tot ${appName}`)}`
    }
  }
  return ''
}

// ─── Cluster composable ───────────────────────────────────────────────────────

export interface ClusterAPI {
  id: string
  name: string
  description: string
  scope: string
  state: string
  agent_version: string
  agent_cert_serial?: string
  agent_cert_expires_at?: string
  // Expiry of the active agent certificate; null until the next handshake
  // or renewal records it.
  agent_cert_not_after?: string | null
  // Set while an admin-requested credential rotation waits for the agent.
  agent_cert_rotation_requested_at?: string | null
  last_heartbeat_at: string
  registered_at: string
  orchestrator_cluster_name: string
  argocd_project_name?: string
  owning_organization_id?: string
  ingress_class_name?: string
  base_domain?: string
}

export interface ClusterAccessGrant {
  cluster_id: string
  organization_id: string
  organization_name: string
  granted_at: string
  granted_by: string
}

export interface ClusterList {
  items: ClusterAPI[]
  total: number
}

export function useClusters() {
  const { api } = useApi()
  return useAsyncData<ClusterList>(
    'clusters',
    () => api<ClusterList>('/api/v1/clusters'),
    { server: false, default: (): ClusterList => ({ items: [], total: 0 }) },
  )
}

// ─── Audit queue composable ───────────────────────────────────────────────────

export interface AuditQueueResponse {
  items: AuditQueueItem[]
  total: number
  page: number
  page_size: number
}

export function useAuditQueue() {
  const { api } = useApi()
  return useAsyncData<AuditQueueResponse>(
    'audit-queue',
    () => api<AuditQueueResponse>('/api/v1/audit-submissions', {
      query: { state: 'submitted', page_size: 50 },
    }),
    { server: false, default: (): AuditQueueResponse => ({ items: [], total: 0, page: 1, page_size: 50 }) },
  )
}

// ─── My apps composable ───────────────────────────────────────────────────────

export function useMyApps() {
  const { api } = useApi()
  return useAsyncData<App[]>(
    'my-apps',
    async () => {
      const states = ['draft', 'submitted', 'needs_changes', 'published', 'rejected', 'archived']
      const results = await Promise.all(
        states.map(s =>
          api<{ items: AppCardAPI[]; total: number }>('/api/v1/apps', {
            query: { status: s, page_size: 100 },
          }),
        ),
      )
      return results.flatMap(r => (r.items ?? []).map(mapCard))
    },
    { server: false, default: (): App[] => [] },
  )
}
