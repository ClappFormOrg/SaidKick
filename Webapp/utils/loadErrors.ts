// Maps the HTTP status of a failed deployment fetch to the error shown to the
// user, so a missing session or missing permission is not reported as "not found".
export function deploymentLoadError(statusCode?: number): { statusCode: number; statusMessage: string } {
  switch (statusCode) {
    case 401: return { statusCode: 401, statusMessage: 'Log in om deze deployment te bekijken' }
    case 403: return { statusCode: 403, statusMessage: 'Geen toegang tot deze deployment' }
    case 404: return { statusCode: 404, statusMessage: 'Deployment niet gevonden' }
    default:  return { statusCode: statusCode && statusCode >= 400 ? statusCode : 500, statusMessage: 'Deployment laden mislukt' }
  }
}
