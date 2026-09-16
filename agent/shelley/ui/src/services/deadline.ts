// deadline.ts — bounded waits for the IndexedDB message cache.
//
// The cache layer awaits several APIs that have no timeout of their own and,
// in a multi-tab browser, can genuinely wait forever:
//
//   * fetch() for /api/cache-key. On HTTP/1.1 every open tab pins one of the
//     six per-origin sockets with its SSE stream, so a later tab's request is
//     never dispatched — no error, no response, just silence.
//   * indexedDB.open(). If another tab still holds a connection at a lower
//     version, the open fires `blocked` and then waits for that tab to close
//     it, which a tab left open across a deploy never does.
//   * navigator.locks.request(). Waits indefinitely for the grant, so a
//     frozen tab holding the lock blocks every sibling.
//
// Each of those sits between "user focuses a conversation" and "the spinner
// clears", so an unbounded wait there is an unbounded spinner. The cache is
// only ever an optimization, so the right response to a slow cache is to give
// up and use the network — never to wait.

/** Thrown when a bounded wait misses its deadline. */
export class DeadlineExceededError extends Error {
  constructor(what: string, ms: number) {
    super(`${what} exceeded its ${ms}ms deadline`);
    this.name = "DeadlineExceededError";
  }
}

export interface DeadlineOptions<T> {
  /** Label used in the error message, e.g. "indexedDB.open". */
  what: string;
  /**
   * Called with a value that arrives *after* we gave up. The underlying
   * operation can't be cancelled (neither IDB opens nor a fetch queued behind
   * a lock), so late results are real and must be disposed of deliberately:
   * close the late DB connection, discard the superseded key.
   */
  onLate?: (value: T) => void;
  /**
   * Called with an error that arrives after we gave up. Never omit this
   * silently — swallowing it recreates the failure this module exists to
   * kill, a cache that quietly stopped working with nothing to say why.
   */
  onLateError?: (err: unknown) => void;
}

/**
 * In-flight bounded waits, for diagnosing a hang WHILE it is hanging.
 *
 * cacheDiag records a decision once it completes, which is exactly the wrong
 * time for this class of bug: a tab stuck on the spinner has, by definition,
 * completed nothing, so its stats() is empty. That silence is what made the
 * original multi-tab hang so hard to place. Registering a wait for its
 * duration means the question "what is this tab waiting on?" has an answer at
 * the moment you need it, from a console, on someone else's machine.
 */
const inFlight = new Set<WaitRecord>();

export interface PendingWait {
  /** Operation label, e.g. "indexedDB.open". */
  what: string;
  /** Deadline it was given, in ms. */
  deadlineMs: number;
  /** How long it has been waiting so far, in ms. */
  elapsedMs: number;
}

interface WaitRecord {
  what: string;
  deadlineMs: number;
  startedAt: number;
}

/** Snapshot of the waits currently outstanding, longest-waiting first. */
export function pendingWaits(): PendingWait[] {
  const now = Date.now();
  return [...inFlight]
    .map((w) => ({ what: w.what, deadlineMs: w.deadlineMs, elapsedMs: now - w.startedAt }))
    .sort((a, b) => b.elapsedMs - a.elapsedMs);
}

/**
 * Await `p`, rejecting with DeadlineExceededError after `ms`.
 *
 * `p` keeps running; see DeadlineOptions.onLate.
 */
export function withDeadline<T>(p: Promise<T>, ms: number, opts: DeadlineOptions<T>): Promise<T> {
  return new Promise<T>((resolve, reject) => {
    let settled = false;
    const entry: WaitRecord = { what: opts.what, deadlineMs: ms, startedAt: Date.now() };
    inFlight.add(entry);
    // Every exit path must deregister, or the diagnostic becomes a liar that
    // reports phantom waits outliving the code that started them.
    const done = () => {
      settled = true;
      inFlight.delete(entry);
    };
    const timer = setTimeout(() => {
      if (settled) return;
      done();
      reject(new DeadlineExceededError(opts.what, ms));
    }, ms);
    p.then(
      (v) => {
        if (settled) {
          // Guarded: this runs in a promise chain nobody awaits, so a throwing
          // callback would surface as an unhandled rejection.
          try {
            opts.onLate?.(v);
          } catch (err) {
            console.warn("deadline: onLate threw", err);
          }
          return;
        }
        done();
        clearTimeout(timer);
        resolve(v);
      },
      (err) => {
        if (settled) {
          try {
            opts.onLateError?.(err);
          } catch (e) {
            console.warn("deadline: onLateError threw", e);
          }
          return;
        }
        done();
        clearTimeout(timer);
        reject(err);
      },
    );
  });
}

/** True when `err` came from a missed deadline rather than a real failure. */
export function isDeadlineExceeded(err: unknown): boolean {
  return err instanceof DeadlineExceededError;
}
