// Azure Functions v4 adapter: HttpRequest -> pure handler -> HttpResponseInit.
import { lowerHeaders, runHandler } from './http.js'

export function adapt(handler) {
  return async (request) => {
    const res = await runHandler(handler, {
      method: request.method,
      headers: lowerHeaders(request.headers.entries()),
      query: Object.fromEntries(request.query.entries()),
      params: { ...request.params },
      readBody: () => request.text(),
    })
    return { status: res.status, headers: res.headers, jsonBody: res.jsonBody }
  }
}
