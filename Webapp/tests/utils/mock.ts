import type { MeResponse } from '~/composables/useAuth'
import type { AppCardAPI, AppDetailAPI } from '~/composables/useData'

export function createMockAppCardAPI(overrides: Partial<AppCardAPI> = {}): AppCardAPI {
  return {
    id: 'app-1',
    slug: 'test-app',
    vendor: 'Test Vendor',
    state: 'published',
    github_repo: 'org/test-app',
    display_name: 'Test App',
    categories: ['chatbot'],
    risk_class: 'limited',
    license: 'MIT',
    last_published_at: '2024-06-01T00:00:00.000Z',
    auto_validation_state: 'passed',
    audit_approved: false,
    ...overrides,
  }
}

export function createMockAppDetailAPI(overrides: Partial<AppDetailAPI> = {}): AppDetailAPI {
  return {
    ...createMockAppCardAPI(),
    owning_organization_id: 'org-1',
    owning_user_id: 'user-1',
    github_installation_id: 12345,
    github_default_branch: 'main',
    repo_link_state: 'active',
    created_at: '2024-01-01T00:00:00.000Z',
    updated_at: '2024-06-01T00:00:00.000Z',
    versie: '1.2.0',
    installs: 42,
    manifest: {
      metadata: {
        name: 'test-app',
        display_name: 'Test App (Manifest)',
        vendor: 'Manifest Vendor',
        contact: 'contact@example.com',
        license: 'Apache-2.0',
      },
      spec: {
        categories: ['chatbot', 'rag'],
        ai: { risk_class: 'limited', base_model: 'gpt-4', data_residency: 'eu' },
        audit: {
          dpia: './docs/dpia.pdf',
          security_scan: './docs/security.pdf',
        },
        interfaces: [{ type: 'api', openapi: './openapi.yaml' }],
      },
    },
    ...overrides,
  }
}

export function createMockMeResponse(overrides: Partial<MeResponse> = {}): MeResponse {
  return {
    id: 'user-1',
    email: 'test@example.com',
    name: 'Test User',
    roles: ['developer'],
    org_slug: 'apps-dev',
    ...overrides,
  }
}
