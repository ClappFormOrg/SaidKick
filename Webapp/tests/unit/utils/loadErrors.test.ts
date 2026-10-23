import { describe, it, expect } from 'vitest'
import { deploymentLoadError } from '~/utils/loadErrors'

describe('deploymentLoadError', () => {
  it('reports 404 as not found', () => {
    expect(deploymentLoadError(404)).toEqual({ statusCode: 404, statusMessage: 'Deployment niet gevonden' })
  })

  it('reports 403 as forbidden, not as not found', () => {
    expect(deploymentLoadError(403)).toEqual({ statusCode: 403, statusMessage: 'Geen toegang tot deze deployment' })
  })

  it('reports 401 as a login prompt', () => {
    expect(deploymentLoadError(401)).toEqual({ statusCode: 401, statusMessage: 'Log in om deze deployment te bekijken' })
  })

  it('keeps other error statuses with a generic message', () => {
    expect(deploymentLoadError(502)).toEqual({ statusCode: 502, statusMessage: 'Deployment laden mislukt' })
  })

  it('falls back to 500 when the status is missing', () => {
    expect(deploymentLoadError(undefined)).toEqual({ statusCode: 500, statusMessage: 'Deployment laden mislukt' })
  })
})
