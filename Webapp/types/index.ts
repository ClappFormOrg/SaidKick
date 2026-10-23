export interface App {
  id?: string
  slug: string
  name: string
  tagline: string
  description: string
  longDescription: string[]
  category: string
  status: string
  tags: string[]
  eigenaar: string
  medeontwikkelaars: string[]
  versie: string
  bijgewerkt: string
  licentie: string
  repo: string
  scores: Record<string, 'pass' | 'partial' | 'fail' | 'pending'>
  riskCategory: string
  installs: number
  currentVersionId?: string
  docs?: { label: string; url: string }[]
  // Evidence document URL per admission criterion key, from the manifest.
  evidence?: Record<string, string>
  screenshots?: string[]
  descriptionUrl?: string
  // This app's live deployments in the signed-in user's own organisation.
  // Empty for anonymous visitors.
  myDeployments?: MyDeploymentRef[]
  // Support address or helpdesk URL from the manifest.
  vendorContact?: string
}

// One live deployment of an app: enough to open it, or to say why not.
export interface MyDeploymentRef {
  deploymentId: string
  availability: 'available' | 'degraded' | 'preparing' | 'unavailable'
  // Present only when the deployment has a web interface and is up.
  url?: string
}

export interface Category {
  slug: string
  title: string
  icon: string
}

export interface Criterion {
  key: string
  label: string
  desc: string
}
