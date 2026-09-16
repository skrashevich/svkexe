// cryptoKey.ts — client side of the IndexedDB encryption scheme.
//
// Holds the per-browser AES-GCM key handed out by GET /api/cache-key, and
// exposes wrap/unwrap helpers used by messageStore.
//
// The key is imported with extractable=false so that the raw bytes cannot
// be re-exported from JS after import; the wire-format bytes only ever live
// in a local variable during fetch().
//
// Threat-model summary: see server/cache_key.go. Not end-to-end. Storing
// encrypted-at-rest in IDB so a stolen browser profile / shared OS account
// without a live auth session can't read prior conversations.

import { cacheDiag } from "./cacheDiag";
import { withDeadline, isDeadlineExceeded } from "./deadline";

export interface CacheKeyMaterial {
  keyId: string;
  key: CryptoKey;
  /** Alg returned by server, e.g. "AES-GCM-256". For diagnostic logging. */
  alg: string;
}

export interface CacheKeyFetcher {
  /** Fetch+import a fresh key. Throws on any error so the caller can fall
   * back to network-only mode. */
  fetch(): Promise<CacheKeyMaterial>;
  /** Tell the server to wipe its session (logout / clear-cache). */
  clear(): Promise<void>;
}

interface FetchedKeyJSON {
  key_id: string;
  key: string; // base64-std, 32 bytes
  alg: string;
}

function b64decode(s: string): Uint8Array {
  const bin = atob(s);
  const out = new Uint8Array(bin.length);
  for (let i = 0; i < bin.length; i++) out[i] = bin.charCodeAt(i);
  return out;
}

/**
 * How long to wait for /api/cache-key before treating the cache as
 * unavailable for this attempt.
 *
 * This bound is load-bearing, not defensive politeness. Every conversation
 * load funnels through CacheKeyHolder.ensure() (hydrate() awaits it before it
 * can read the IDB row) and ChatInterface keeps its spinner up until
 * loadMessages returns — so an unbounded wait here is an unbounded spinner
 * there. With several tabs open on one origin that wait is reachable in
 * practice: on HTTP/1.1 each tab pins one of the six per-origin sockets with
 * its SSE stream, and a later tab's key request simply never gets dispatched.
 *
 * Timing out is cheap: the cache is an optimization, so giving up just means
 * this load goes to the network.
 */
export const CACHE_KEY_TIMEOUT_MS = 10_000;

/**
 * Deadline for the HTTP request itself, deliberately shorter than
 * CACHE_KEY_TIMEOUT_MS.
 *
 * The two deadlines are layered — the fetch aborts itself, and ensure() gives
 * up on the whole attempt — and they must not fire in the wrong order. If the
 * outer one won, ensure() would abandon an attempt that was about to fail
 * cleanly on its own, so the inner one gets enough headroom to finish first.
 * The gap also covers the part of an attempt no AbortController can reach:
 * waiting to be granted the cross-tab lock.
 */
const CACHE_KEY_FETCH_TIMEOUT_MS = 7_000;

/**
 * How long to wait to be granted the cross-tab cache-key lock.
 *
 * navigator.locks.request() waits for the grant indefinitely, and the holder
 * may be a tab Safari has frozen (frozen pages keep their locks — only unload
 * releases them). The lock is an optimization, so a contended grant must fall
 * through to an unsynchronized fetch rather than become a new way to hang.
 */
const CACHE_KEY_LOCK_TIMEOUT_MS = 2_000;

/**
 * fetch() with a hard deadline. AbortSignal.timeout() would be tidier but
 * isn't available in every browser we support.
 *
 * The signal stays armed while the caller reads the body, not just until the
 * headers land: a response whose body never completes would otherwise hold the
 * cross-tab lock for as long as the connection lives. `dispose()` releases it.
 */
function fetchWithTimeout(
  url: string,
  init: RequestInit,
): { response: Promise<Response>; dispose: () => void } {
  const ctl = new AbortController();
  const timer = setTimeout(() => ctl.abort(), CACHE_KEY_FETCH_TIMEOUT_MS);
  let response: Promise<Response>;
  try {
    response = fetch(url, { ...init, signal: ctl.signal });
  } catch (err) {
    // A synchronous throw (bad URL, some polyfills) never reaches the caller's
    // finally, so clear the timer here or it runs on to abort nothing.
    clearTimeout(timer);
    throw err;
  }
  return { response, dispose: () => clearTimeout(timer) };
}

/**
 * Name of the cross-tab Web Lock held while minting a cache session.
 *
 * The cache cookie is HttpOnly, so a tab cannot ask "does this browser already
 * have a cache session?" — it can only make the request and see what comes
 * back. The server, in turn, mints a brand-new cookie and key for any request
 * that arrives without one. Put those together and a browser that restores
 * several tabs at once with no cookie yet (Safari reopening a window, or a
 * first visit that opens several tabs) has every tab minting its own session:
 * they share one cookie jar but hold different keys, so each reads keys_meta,
 * sees a stranger's key_id, and wipes the shared IDB stores the others just
 * filled. Net effect: every tab re-downloads its whole conversation,
 * repeatedly, while fighting the others.
 *
 * Serializing the request fixes it with no server-side coordination: the first
 * tab mints the cookie, and every tab after it presents that cookie and so
 * derives the same key.
 *
 * This is mitigation, not a guarantee. The underlying defect is a GET that
 * mints state; clients without Web Locks (and non-browser clients) still race.
 * A server-side fix — deriving the salt from the authenticated session, or
 * making the mint idempotent per session — would fix every client at once.
 */
const CACHE_KEY_LOCK = "shelley-cache-key";

/** Set once any fetch has seen a key, i.e. the cookie now exists. */
let cacheSessionEstablished = false;

/**
 * Run `fn` under the cross-tab cache-key lock.
 *
 * Only the COLD START needs serializing: the race is over minting the cookie,
 * so once any tab holds a key every later request presents that cookie and
 * there is nothing to coordinate. Skipping the lock in steady state keeps it
 * off the hot path, which matters because a socket-starved tab holds the lock
 * for the whole duration of its doomed request and would otherwise queue every
 * sibling behind it.
 *
 * Falls back to running `fn` unlocked when Web Locks are unavailable (older
 * Safari, non-secure contexts, tests) or when the grant doesn't arrive
 * promptly. A duplicated key costs a re-download, not correctness, so a
 * contended lock must never become a new way to hang.
 */
async function withCacheKeyLock<T>(fn: () => Promise<T>): Promise<T> {
  const locks = (globalThis.navigator as Navigator | undefined)?.locks;
  if (cacheSessionEstablished || !locks?.request) return fn();
  const ctl = new AbortController();
  const timer = setTimeout(() => ctl.abort(), CACHE_KEY_LOCK_TIMEOUT_MS);
  // Whether we were granted the lock, as opposed to still queueing for it.
  // `signal.aborted` is NOT a usable substitute: the signal only dequeues a
  // pending request, so once the callback is running an abort is a no-op on
  // the lock but still flips the flag. Treating that as "never granted" would
  // re-run fn() after a slow-but-granted attempt failed — a duplicate request
  // on exactly the cold-start path where duplicates cause key divergence.
  let granted = false;
  try {
    return (await locks.request(CACHE_KEY_LOCK, { signal: ctl.signal }, () => {
      granted = true;
      clearTimeout(timer);
      return fn();
    })) as T;
  } catch (err) {
    if (granted) throw err; // a real failure inside fn
    const name = err instanceof Error ? err.name : "";
    if (ctl.signal.aborted || name === "AbortError") {
      // We never got the grant. Proceed unlocked rather than failing the load:
      // a duplicated key costs a re-download, not correctness, so a contended
      // lock must not become a new way to hang.
      cacheDiag("info", "cache_key.lock_contended", { timeout_ms: CACHE_KEY_LOCK_TIMEOUT_MS });
      return fn();
    }
    if (name === "NotSupportedError" || name === "SecurityError") {
      // Some contexts (sandboxed/opaque origins) reject the request outright
      // instead of simply not exposing navigator.locks.
      cacheDiag("info", "cache_key.lock_unavailable", { error: String(err) });
      return fn();
    }
    throw err;
  } finally {
    clearTimeout(timer);
  }
}

/** Production fetcher hitting /api/cache-key on the same origin. */
export class HttpCacheKeyFetcher implements CacheKeyFetcher {
  constructor(private readonly endpoint = "/api/cache-key") {}
  async fetch(): Promise<CacheKeyMaterial> {
    return withCacheKeyLock(async () => {
      const { response, dispose } = fetchWithTimeout(this.endpoint, {
        method: "GET",
        credentials: "include",
        cache: "no-store",
      });
      try {
        const r = await response;
        if (!r.ok) throw new Error(`cache-key: HTTP ${r.status}`);
        const body = (await r.json()) as FetchedKeyJSON;
        const material = await importMaterial(body);
        // A cookie exists now, so later fetches can skip the lock.
        cacheSessionEstablished = true;
        return material;
      } finally {
        dispose();
      }
    });
  }
  async clear(): Promise<void> {
    // Hold the lock across the clear so a sibling cannot mint a session
    // against the cookie we are invalidating and end up holding a key the
    // server has already forgotten.
    cacheSessionEstablished = false;
    await withCacheKeyLock(async () => {
      const { response, dispose } = fetchWithTimeout("/api/cache-session/clear", {
        method: "POST",
        credentials: "include",
        cache: "no-store",
      });
      try {
        const r = await response;
        if (!r.ok) {
          // Don't silently downgrade: if the server didn't actually rotate
          // the cookie/session, returning success would leave the next
          // GET /api/cache-key handing back the same key with the same
          // key_id, defeating rotation. Caller (wipeAndRotateKey) catches
          // and logs.
          throw new Error(`cache-session/clear: HTTP ${r.status}`);
        }
      } finally {
        dispose();
      }
    });
  }
}

/** Import a server response into a non-extractable CryptoKey. */
export async function importMaterial(body: FetchedKeyJSON): Promise<CacheKeyMaterial> {
  if (!body.key_id || !body.key || !body.alg) {
    throw new Error("cache-key: malformed response");
  }
  const raw = b64decode(body.key);
  if (raw.byteLength !== 32) {
    throw new Error(`cache-key: bad key length ${raw.byteLength}`);
  }
  const buf = new ArrayBuffer(raw.byteLength);
  new Uint8Array(buf).set(raw);
  const key = await crypto.subtle.importKey(
    "raw",
    buf,
    { name: "AES-GCM" },
    /* extractable */ false,
    ["encrypt", "decrypt"],
  );
  // Best-effort zero of both local buffers. V8 may still have copies
  // and the underlying WebCrypto implementation may also retain its
  // own; this is intent more than guarantee.
  raw.fill(0);
  new Uint8Array(buf).fill(0);
  return { keyId: body.key_id, key, alg: body.alg };
}

// ─── AES-GCM wrap/unwrap helpers ─────────────────────────────────────────────

const IV_BYTES = 12; // AES-GCM nominal IV size
const textEncoder = new TextEncoder();
const textDecoder = new TextDecoder();

/**
 * Wrap a JSON-serializable value into { iv, ct }. Random IV per call.
 *
 * Note: SubtleCrypto's TS bindings reject `Uint8Array<ArrayBufferLike>`
 * because the underlying buffer might be SharedArrayBuffer. We copy into
 * a fresh ArrayBuffer-backed view via `toArrayBuffer()` to make the types
 * check and to defend against shared-buffer aliasing.
 */
function toArrayBuffer(view: Uint8Array): ArrayBuffer {
  const out = new ArrayBuffer(view.byteLength);
  new Uint8Array(out).set(view);
  return out;
}

export async function wrapJSON(
  key: CryptoKey,
  value: unknown,
  aad?: Uint8Array,
): Promise<{ iv: Uint8Array; ct: Uint8Array }> {
  const iv = crypto.getRandomValues(new Uint8Array(IV_BYTES));
  const pt = textEncoder.encode(JSON.stringify(value));
  const params: AesGcmParams = { name: "AES-GCM", iv: toArrayBuffer(iv) };
  if (aad) params.additionalData = toArrayBuffer(aad);
  const ctBuf = await crypto.subtle.encrypt(params, key, toArrayBuffer(pt));
  return { iv, ct: new Uint8Array(ctBuf) };
}

/**
 * Inverse of wrapJSON. Throws on auth-tag failure (wrong key / tampered /
 * AAD mismatch).
 *
 * Pass the same `aad` that was used at encryption time; mismatches surface
 * as decrypt failure (caller treats as undecryptable row).
 */
export async function unwrapJSON<T>(
  key: CryptoKey,
  iv: Uint8Array,
  ct: Uint8Array,
  aad?: Uint8Array,
): Promise<T> {
  const params: AesGcmParams = { name: "AES-GCM", iv: toArrayBuffer(iv) };
  if (aad) params.additionalData = toArrayBuffer(aad);
  const ptBuf = await crypto.subtle.decrypt(params, key, toArrayBuffer(ct));
  return JSON.parse(textDecoder.decode(ptBuf)) as T;
}

/**
 * Build a stable AAD byte string for a row. Binding the plaintext index
 * fields into AES-GCM's additional-authenticated-data prevents an attacker
 * with IDB write access from splicing a valid {iv,ct} blob from one row
 * onto another row's plaintext keys (e.g. swapping message bodies between
 * conversations). The AAD is not encrypted, only authenticated.
 *
 * Versioned with a leading tag so we can change the layout without
 * silently invalidating existing rows (mismatch reads as decrypt failure,
 * which we treat as undecryptable → effectively a per-row wipe).
 */
export function rowAAD(parts: Record<string, string | number>): Uint8Array {
  // Deterministic key order so wrap and unwrap agree without relying on
  // insertion order at the call site.
  const keys = Object.keys(parts).sort();
  const canonical: Record<string, string | number> = {};
  for (const k of keys) canonical[k] = parts[k];
  return textEncoder.encode("shelley-idb-aad-v1:" + JSON.stringify(canonical));
}

// ─── Single-tab key holder ───────────────────────────────────────────────────
//
// Most call sites only need a singleton view. Tests inject their own holder.

/**
 * One attempt to fetch the key.
 *
 * Timeout state is a property of the attempt, not of the holder. Keeping them
 * together is what makes the state machine safe: a caller that gives up can
 * only ever mark the attempt IT was waiting on, so a slow caller can't apply
 * its verdict to a newer attempt (which would abandon a perfectly good fetch
 * and start a duplicate — and duplicate requests are exactly what exhausts
 * the socket pool this class is trying to survive).
 */
interface KeyAttempt {
  /** Resolves to the material, or null if the fetch failed. Never rejects. */
  promise: Promise<CacheKeyMaterial | null>;
  /** When a caller gave up on this attempt; 0 while it is still awaited. */
  timedOutAt: number;
  /** Wall clock at kick-off, so late joiners share the attempt's deadline. */
  startedAt: number;
}

export class CacheKeyHolder {
  private material: CacheKeyMaterial | null = null;
  private attempt: KeyAttempt | null = null;
  private readonly timeoutMs: number;

  /**
   * `timeoutMs` exists so tests can assert the give-up behaviour without
   * sleeping for the production deadline; production callers omit it.
   */
  constructor(
    private readonly fetcher: CacheKeyFetcher,
    timeoutMs: number = CACHE_KEY_TIMEOUT_MS,
  ) {
    this.timeoutMs = timeoutMs;
  }

  /**
   * Acquire (or return) the key. Returns null when the server refuses, when
   * the request fails, or when it doesn't answer within `timeoutMs`.
   *
   * The deadline lives here as well as in HttpCacheKeyFetcher because this is
   * the choke point every cache read passes through: whatever fetcher is
   * injected, callers of ensure() must get an answer. Null means "cache
   * unavailable, use the network" — the same path as an outright refusal.
   *
   * A timed-out fetch is left running (nothing here can cancel a request
   * queued behind an exhausted socket pool) but we deliberately do NOT wait on
   * it a second time. Re-racing a promise we already gave up on would make
   * every later hydrate pay the full timeout again, turning a stranded tab
   * into a uselessly slow one. Instead we answer null immediately until the
   * cooldown expires, then start a genuinely new attempt.
   */
  async ensure(): Promise<CacheKeyMaterial | null> {
    if (this.material) return this.material;
    let attempt = this.attempt;
    if (attempt && attempt.timedOutAt !== 0) {
      if (Date.now() - attempt.timedOutAt < this.timeoutMs) {
        // Still cooling down from a give-up. Answer now: the caller's job is
        // to render from the network, not to queue behind a request that may
        // never land.
        return null;
      }
      // Cooldown expired and it still hasn't landed. Abandon it (see
      // startAttempt: dropping our reference is what makes its result stale)
      // and try again from scratch.
      attempt = null;
    }
    if (!attempt) attempt = this.startAttempt();
    // Late joiners share the attempt's deadline rather than restarting the
    // clock, so the bound is a property of the tab, not of the call.
    const remaining = Math.max(0, this.timeoutMs - (Date.now() - attempt.startedAt));
    try {
      return await withDeadline(attempt.promise, remaining, {
        what: "GET /api/cache-key",
        // A late key is still worth installing (startAttempt does it, under
        // its staleness check) so the NEXT hydrate is a hit, not a round trip.
        onLate: (m) => cacheDiag("info", "cache_key.late_arrival", { installed: m !== null }),
        onLateError: (err) => cacheDiag("fail", "cache_key.late_failure", { error: String(err) }),
      });
    } catch (err) {
      if (isDeadlineExceeded(err)) {
        // Mark the attempt WE waited on. If it has already been superseded,
        // this is a no-op on the current one, which is the point.
        if (attempt.timedOutAt === 0) attempt.timedOutAt = Date.now();
        cacheDiag("fail", "cache_key.timeout", { timeout_ms: this.timeoutMs });
        return null;
      }
      // startAttempt converts failures to null and logs them, so this is
      // unreachable in practice; be defensive anyway.
      cacheDiag("fail", "cache_key.unavailable", { error: String(err) });
      return null;
    }
  }

  /**
   * Kick off a fetch and install it as the current attempt.
   *
   * The result is applied only if this attempt is still the current one.
   * Anything else means the key we asked for has been superseded — by a
   * rotation, or because a caller timed out and we moved on — and installing
   * it would leave `material` naming an older key than keys_meta does, so the
   * next openAndSyncKey would wipe the shared stores. That is precisely the
   * cross-tab thrash the cache-key lock exists to prevent.
   */
  private startAttempt(): KeyAttempt {
    const a: KeyAttempt = {
      promise: Promise.resolve(null),
      timedOutAt: 0,
      startedAt: Date.now(),
    };
    a.promise = this.fetcher
      .fetch()
      .then((m) => {
        if (this.attempt !== a) {
          cacheDiag("info", "cache_key.stale_result_dropped", { key_id: m.keyId });
          return null;
        }
        this.material = m;
        // The attempt succeeded, so nothing is cooling down any more.
        a.timedOutAt = 0;
        return m;
      })
      .catch((err) => {
        cacheDiag("fail", "cache_key.unavailable", { error: String(err) });
        if (this.attempt === a) {
          this.material = null;
          // A settled failure is not a pending request, so drop it: the next
          // ensure() must start a new attempt immediately rather than sit out
          // a cooldown for a request that is already over.
          this.attempt = null;
        }
        return null;
      });
    this.attempt = a;
    return a;
  }

  current(): CacheKeyMaterial | null {
    return this.material;
  }

  /** Drop the in-memory key. Used after server-side clear and on logout. */
  forget(): void {
    this.material = null;
    // Any fetch still in flight was started for the key we're dropping.
    // Dropping our reference to it makes its result stale (see startAttempt),
    // so it can't install itself after the fact.
    this.attempt = null;
    // The cookie may be gone, so the next cold fetch has to take the cache-key
    // lock again rather than assuming a session already exists.
    cacheSessionEstablished = false;
  }

  async clear(): Promise<void> {
    await this.fetcher.clear();
    this.forget();
  }
}
