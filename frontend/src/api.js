export async function request(url, options = {}) {
  const response = await fetch(url, options)
  if (response.status === 204) return null
  const payload = await response.json().catch(() => ({}))
  if (!response.ok) throw new Error(payload.error || `Request failed (${response.status})`)
  return payload
}

export function jsonOptions(method, body) {
  return {
    method,
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  }
}
