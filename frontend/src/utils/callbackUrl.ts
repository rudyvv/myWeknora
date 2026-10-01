export function buildApiCallbackUrl(callbackPath: string, apiBase: string, origin: string): string {
  const base = new URL(apiBase || '/', origin)
  const prefix = base.pathname.replace(/\/+$/, '')
  const suffix = `/${callbackPath.replace(/^\/+/, '')}`
  base.pathname = `${prefix}${suffix}`.replace(/\/{2,}/g, '/')
  base.search = ''
  base.hash = ''
  return base.toString()
}
