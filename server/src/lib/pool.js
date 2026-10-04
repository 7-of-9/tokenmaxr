// Bounded concurrency for storage calls. Only leaf storage operations take a
// slot, so nested Promise.all fan-out can never deadlock the limiter.
export function limiter(max) {
  let active = 0
  const queue = []
  const next = () => {
    if (active >= max || queue.length === 0) return
    active++
    const { fn, resolve, reject } = queue.shift()
    Promise.resolve()
      .then(fn)
      .then(resolve, reject)
      .finally(() => {
        active--
        next()
      })
  }
  return (fn) =>
    new Promise((resolve, reject) => {
      queue.push({ fn, resolve, reject })
      next()
    })
}

// A wall-clock budget, independent of the request's logical `now`.
export function deadline(ms) {
  const end = Date.now() + ms
  return {
    expired: (reserveMs = 0) => Date.now() + reserveMs >= end,
    remaining: () => end - Date.now(),
  }
}

export class DeadlineError extends Error {
  constructor() {
    super('time budget exhausted')
    this.name = 'DeadlineError'
  }
}
