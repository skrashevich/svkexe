// cryptoKey tests — the cache-key fetch must never strand a caller.
//
// Every conversation load funnels through CacheKeyHolder.ensure(): hydrate()
// awaits it before it can read (or prove absent) the IDB row, and
// ChatInterface only clears its spinner once loadMessages returns. So an
// unbounded wait here is an unbounded spinner there. With several Safari tabs
// on one origin that wait is reachable: on HTTP/1.1 every tab pins one of the
// six per-origin sockets with its SSE stream, and a later tab's key request is
// never dispatched. That is the multi-tab "some tabs hang while loading"
// report these tests pin down.
//
// Deadlines are injected (tens of ms) so the give-up behaviour is asserted
// without sleeping for the production timeout.
//
// Run via `pnpm test` (see scripts/run-tests.mjs).

import { webcrypto } from "node:crypto";
import {
  CacheKeyHolder,
  HttpCacheKeyFetcher,
  type CacheKeyFetcher,
  type CacheKeyMaterial,
} from "./cryptoKey";

if (typeof globalThis.crypto === "undefined" || !globalThis.crypto.subtle) {
  Object.defineProperty(globalThis, "crypto", { value: webcrypto, configurable: true });
}

/** Short enough to keep the suite fast, long enough not to race the event loop. */
const TIMEOUT_MS = 60;

function assert(cond: boolean, msg: string): void {
  if (!cond) throw new Error(`Assertion failed: ${msg}`);
}
async function run(name: string, fn: () => Promise<void>): Promise<void> {
  try {
    await fn();
    console.log(`\u2713 ${name}`);
  } catch (err) {
    console.error(`\u2717 ${name}`);
    throw err;
  }
}
const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms));

async function makeMaterial(keyId: string): Promise<CacheKeyMaterial> {
  const buf = new ArrayBuffer(32);
  crypto.getRandomValues(new Uint8Array(buf));
  const key = await crypto.subtle.importKey("raw", buf, { name: "AES-GCM" }, false, [
    "encrypt",
    "decrypt",
  ]);
  return { keyId, key, alg: "AES-GCM-256" };
}

/** A fetcher whose every call is resolved by the test, in order. */
class GatedFetcher implements CacheKeyFetcher {
  calls = 0;
  private waiters: Array<(m: CacheKeyMaterial) => void> = [];
  private rejecters: Array<(e: unknown) => void> = [];
  fetch(): Promise<CacheKeyMaterial> {
    this.calls++;
    return new Promise<CacheKeyMaterial>((res, rej) => {
      this.waiters.push(res);
      this.rejecters.push(rej);
    });
  }
  async clear(): Promise<void> {}
  /** Resolve the Nth (0-based) outstanding call. */
  async resolve(n: number, keyId: string): Promise<void> {
    this.waiters[n](await makeMaterial(keyId));
    await sleep(0);
  }
  async reject(n: number, err: unknown): Promise<void> {
    this.rejecters[n](err);
    await sleep(0);
  }
}

/** Swap globalThis.fetch for the duration of `fn`, always restoring it. */
async function withFakeFetch(fake: typeof globalThis.fetch, fn: () => Promise<void>) {
  const orig = globalThis.fetch;
  Object.defineProperty(globalThis, "fetch", {
    value: fake,
    configurable: true,
    writable: true,
  });
  try {
    await fn();
  } finally {
    Object.defineProperty(globalThis, "fetch", {
      value: orig,
      configurable: true,
      writable: true,
    });
  }
}

/**
 * A Web Locks stand-in that records acquire/release order, so a test can prove
 * requests were serialized rather than merely observing a lucky outcome.
 * Always restore the previous navigator: leaving it installed would leak into
 * other test files sharing this process.
 */
function withWebLocks(fn: (held: () => string[]) => Promise<void>): Promise<void> {
  const chains = new Map<string, Promise<unknown>>();
  const log: string[] = [];
  const locks = {
    request: async (name: string, _opts: unknown, cb?: () => Promise<unknown>) => {
      const body = (typeof _opts === "function" ? _opts : cb) as () => Promise<unknown>;
      const prev = chains.get(name) ?? Promise.resolve();
      const mine = prev.then(async () => {
        log.push(`acquire:${name}`);
        try {
          return await body();
        } finally {
          log.push(`release:${name}`);
        }
      });
      chains.set(
        name,
        mine.catch(() => {}),
      );
      return mine;
    },
  };
  const had = Object.prototype.hasOwnProperty.call(globalThis, "navigator");
  const orig = had ? globalThis.navigator : undefined;
  Object.defineProperty(globalThis, "navigator", {
    value: { ...(orig ?? {}), locks },
    configurable: true,
  });
  const restore = () => {
    if (had) {
      Object.defineProperty(globalThis, "navigator", { value: orig, configurable: true });
    } else {
      delete (globalThis as { navigator?: unknown }).navigator;
    }
  };
  return fn(() => log.slice()).finally(restore);
}

async function main(): Promise<void> {
  await run("ensure() gives up instead of hanging when the fetch stalls", async () => {
    // The bug: the key fetch had no deadline, so a request Safari never
    // dispatched left ensure() pending forever and the spinner never cleared.
    const holder = new CacheKeyHolder(
      { fetch: () => new Promise(() => {}), clear: async () => {} },
      TIMEOUT_MS,
    );
    const started = Date.now();
    const got = await Promise.race([
      holder.ensure(),
      sleep(TIMEOUT_MS * 20).then(() => "hung" as const),
    ]);
    assert(got !== "hung", "ensure() must settle rather than hang forever");
    assert(got === null, "and report the cache as unavailable");
    assert(Date.now() - started < TIMEOUT_MS * 10, "must give up near the configured deadline");
  });

  await run("a timed-out attempt is not re-awaited by later callers", async () => {
    // Regression guard for a subtle way of "fixing" the hang badly: if the
    // give-up leaves the dead promise installed as `pending`, every later
    // ensure() re-races it and pays the full deadline again. The tab is no
    // longer stranded but every conversation switch stalls for the timeout,
    // which is barely better than the original bug.
    const holder = new CacheKeyHolder(
      { fetch: () => new Promise(() => {}), clear: async () => {} },
      TIMEOUT_MS,
    );
    assert((await holder.ensure()) === null, "first attempt times out");
    const t = Date.now();
    assert((await holder.ensure()) === null, "second attempt also reports unavailable");
    assert(
      Date.now() - t < TIMEOUT_MS,
      `second call must answer promptly, took ${Date.now() - t}ms`,
    );
  });

  await run("the cache recovers on the same holder once the server answers", async () => {
    // The property that makes timing out safe: it must be retryable on the
    // very holder that failed. If a null were latched, one blip would disable
    // the cache for the life of the tab.
    const g = new GatedFetcher();
    const holder = new CacheKeyHolder(g, TIMEOUT_MS);
    assert((await holder.ensure()) === null, "first attempt times out");
    // Wait out the cooldown, then let the retry succeed.
    await sleep(TIMEOUT_MS + 10);
    const pending = holder.ensure();
    await sleep(0);
    assert(g.calls === 2, `expected a genuinely new fetch, calls=${g.calls}`);
    await g.resolve(1, "kid-recovered");
    const got = await pending;
    assert(got !== null && got.keyId === "kid-recovered", "recovers with the fresh key");
    assert((await holder.ensure()) === got, "and caches it from then on");
  });

  await run("a rejected fetch is retried, not remembered", async () => {
    const g = new GatedFetcher();
    const holder = new CacheKeyHolder(g, TIMEOUT_MS);
    const first = holder.ensure();
    await g.reject(0, new Error("boom"));
    assert((await first) === null, "a failed fetch reports unavailable");
    const second = holder.ensure();
    await sleep(0);
    assert(g.calls === 2, `failure must not be cached, calls=${g.calls}`);
    await g.resolve(1, "kid-2");
    assert((await second)?.keyId === "kid-2", "second attempt succeeds");
  });

  await run("a key that arrives after we gave up is still installed", async () => {
    // Nothing can cancel a request queued behind an exhausted socket pool, so
    // late responses are real. Installing one makes the NEXT hydrate a cache
    // hit instead of another round trip.
    const g = new GatedFetcher();
    const holder = new CacheKeyHolder(g, TIMEOUT_MS);
    assert((await holder.ensure()) === null, "we gave up");
    await g.resolve(0, "kid-late");
    assert(holder.current()?.keyId === "kid-late", "the late key is adopted");
    assert((await holder.ensure())?.keyId === "kid-late", "and served without another fetch");
    assert(g.calls === 1, `no extra fetch should be needed, calls=${g.calls}`);
  });

  await run("a superseded key that arrives late is discarded", async () => {
    // Once a fetch can outlive the call that started it, a stale response can
    // land after a rotation. Installing it would leave the in-memory key older
    // than keys_meta, so the next openAndSyncKey would wipe the shared stores
    // — the cross-tab thrash the cache-key lock exists to prevent.
    const g = new GatedFetcher();
    const holder = new CacheKeyHolder(g, TIMEOUT_MS);
    assert((await holder.ensure()) === null, "first attempt times out");
    holder.forget(); // e.g. a sibling tab broadcast a rotation
    await g.resolve(0, "kid-superseded");
    assert(holder.current() === null, "the pre-rotation key must not be installed");
  });

  await run("concurrent callers share one in-flight fetch", async () => {
    // N conversations hydrating at once must not fire N key requests; on the
    // HTTP/1.1 path the extra requests are what exhaust the socket pool.
    const g = new GatedFetcher();
    const holder = new CacheKeyHolder(g, TIMEOUT_MS * 20);
    const all = Promise.all([holder.ensure(), holder.ensure(), holder.ensure()]);
    await sleep(0);
    assert(g.calls === 1, `expected 1 fetch, got ${g.calls}`);
    await g.resolve(0, "kid-shared");
    const [a, b, c] = await all;
    assert(a === b && b === c && a !== null, "all callers see the same material");
  });

  await run("cold-start key fetches are serialized across tabs", async () => {
    // The cookie is HttpOnly, so a tab can't tell whether a sibling already
    // minted a cache session; it can only ask. The server mints a FRESH cookie
    // and key for any request that arrives without one, so N tabs booting
    // together on a cookie-less browser each get a DIFFERENT key while sharing
    // one cookie jar. Each then sees keys_meta naming a stranger's key and
    // wipes the shared stores, so every tab re-downloads everything.
    await withWebLocks(async (held) => {
      let minted = 0;
      let cookie: string | null = null;
      // Model the server, including the round trip: a request decides what to
      // return only after a tick, so without the lock all six would observe
      // "no cookie" and mint their own. This is what makes the test fail if
      // the lock is removed.
      const fake = (async () => {
        const seen = cookie;
        await sleep(5);
        if (seen === null) {
          minted++;
          cookie = `session-${minted}`;
        }
        return new Response(
          JSON.stringify({
            key_id: `kid-${cookie}`,
            key: btoa(String.fromCharCode(...new Uint8Array(32))),
            alg: "AES-GCM-256",
          }),
          { status: 200 },
        );
      }) as unknown as typeof globalThis.fetch;

      await withFakeFetch(fake, async () => {
        // Six independent "tabs", as separate tabs would be, all asking at once.
        const results = await Promise.all(
          Array.from({ length: 6 }, () => new HttpCacheKeyFetcher().fetch()),
        );
        const distinct = new Set(results.map((r) => r.keyId));
        assert(
          distinct.size === 1,
          `tabs must converge on one key, got ${distinct.size}: ${[...distinct].join(",")}`,
        );
        assert(minted === 1, `server should mint exactly one session, minted ${minted}`);
        const log = held();
        assert(log.length >= 2, "the lock must actually be taken");
        assert(
          log.every((e, i) => (i % 2 === 0 ? e.startsWith("acquire") : e.startsWith("release"))),
          `lock must serialize, not interleave: ${log.join(" ")}`,
        );
      });
    });
  });

  await run("a rejected fetch ends the cooldown immediately", async () => {
    // A settled failure is not a pending request. If the give-up cooldown
    // outlived it, callers would be told "unavailable" for the full timeout
    // even though nothing is in flight and a retry would cost one round trip.
    const g = new GatedFetcher();
    const holder = new CacheKeyHolder(g, TIMEOUT_MS);
    assert((await holder.ensure()) === null, "first attempt times out");
    await g.reject(0, new Error("502"));
    const t = Date.now();
    const pending = holder.ensure();
    await sleep(0);
    assert(g.calls === 2, `a new attempt must start at once, calls=${g.calls}`);
    await g.resolve(1, "kid-after-failure");
    assert((await pending)?.keyId === "kid-after-failure", "and it can succeed");
    assert(Date.now() - t < TIMEOUT_MS, "without sitting out a cooldown");
  });

  await run("one caller's timeout cannot poison a newer attempt", async () => {
    // The trap this guards: if the give-up verdict were recorded on the HOLDER
    // rather than on the attempt it belongs to, a caller whose deadline fires
    // after its own attempt was superseded would stamp the CURRENT attempt
    // instead. Later callers would then be turned away from a perfectly
    // healthy fetch (or start a duplicate — and duplicate requests are what
    // exhausts the socket pool this class exists to survive).
    //
    // Timings are staggered so the healthy attempt still has budget left when
    // the stale verdict lands: A's deadline fires at ~T, while attempt 1 was
    // started at ~T/2 and so runs until ~1.5T.
    const T = 200;
    const g = new GatedFetcher();
    const holder = new CacheKeyHolder(g, T);
    const a = holder.ensure(); // attempt 0, gives up at ~T
    await sleep(0);
    holder.forget(); // a rotation supersedes attempt 0
    await sleep(T / 2);
    const b = holder.ensure(); // attempt 1, healthy, runs until ~1.5T
    await sleep(0);
    assert(g.calls === 2, `attempt 1 should have started, calls=${g.calls}`);
    assert((await a) === null, "A gives up on the superseded attempt");
    // A's verdict has now landed. A caller arriving here is the probe: it must
    // join attempt 1 rather than be told the cache is unavailable.
    const c = holder.ensure();
    await sleep(0);
    assert(g.calls === 2, `C must join attempt 1, not start another, calls=${g.calls}`);
    await g.resolve(1, "kid-healthy");
    assert((await b)?.keyId === "kid-healthy", "the healthy attempt still delivers its key");
    assert((await c)?.keyId === "kid-healthy", "and a later caller gets it too, not null");
    assert(holder.current()?.keyId === "kid-healthy", "and it is the installed key");
  });

  await run("a request that fails after the lock was granted is not re-run", async () => {
    // locks.request's signal only dequeues a PENDING request: once the
    // callback runs, an abort is a no-op on the lock but still flips
    // signal.aborted. So "aborted" must not be read as "never granted" — doing
    // so re-runs the fetch after a slow-but-granted attempt failed, which is a
    // duplicate request on the exact cold-start path where duplicates cause
    // key divergence. Model the race by granting only AFTER the signal has
    // aborted, i.e. the abort arrived too late to dequeue us.
    let calls = 0;
    const locks = {
      request: async (
        _name: string,
        opts: { signal?: AbortSignal },
        cb: () => Promise<unknown>,
      ) => {
        while (!opts.signal?.aborted) await sleep(5); // grant races the abort, and loses
        return cb();
      },
    };
    const had = Object.prototype.hasOwnProperty.call(globalThis, "navigator");
    const orig = had ? globalThis.navigator : undefined;
    Object.defineProperty(globalThis, "navigator", {
      value: { ...(orig ?? {}), locks },
      configurable: true,
    });
    try {
      const fake = (async () => {
        calls++;
        throw new TypeError("network down");
      }) as unknown as typeof globalThis.fetch;
      await withFakeFetch(fake, async () => {
        let err: unknown;
        try {
          await new HttpCacheKeyFetcher().fetch();
        } catch (e) {
          err = e;
        }
        assert(err !== undefined, "the failure propagates to the caller");
        assert(calls === 1, `the request must not be retried unlocked, calls=${calls}`);
      });
    } finally {
      if (had) {
        Object.defineProperty(globalThis, "navigator", { value: orig, configurable: true });
      } else {
        delete (globalThis as { navigator?: unknown }).navigator;
      }
    }
  });

  await run("an uncontended grant that never arrives falls back to unlocked", async () => {
    // The complement: when the abort really does dequeue us, we must proceed
    // without the lock rather than failing the load. A duplicated key costs a
    // re-download, not correctness.
    let calls = 0;
    const locks = {
      request: (_name: string, opts: { signal?: AbortSignal }) =>
        new Promise((_res, rej) => {
          opts.signal?.addEventListener("abort", () =>
            rej(new DOMException("aborted", "AbortError")),
          );
        }),
    };
    const had = Object.prototype.hasOwnProperty.call(globalThis, "navigator");
    const orig = had ? globalThis.navigator : undefined;
    Object.defineProperty(globalThis, "navigator", {
      value: { ...(orig ?? {}), locks },
      configurable: true,
    });
    try {
      const fake = (async () => {
        calls++;
        return new Response(
          JSON.stringify({
            key_id: "kid-unlocked",
            key: btoa(String.fromCharCode(...new Uint8Array(32))),
            alg: "AES-GCM-256",
          }),
          { status: 200 },
        );
      }) as unknown as typeof globalThis.fetch;
      await withFakeFetch(fake, async () => {
        const m = await new HttpCacheKeyFetcher().fetch();
        assert(m.keyId === "kid-unlocked", "the load still completes");
        assert(calls === 1, `exactly one request, calls=${calls}`);
      });
    } finally {
      if (had) {
        Object.defineProperty(globalThis, "navigator", { value: orig, configurable: true });
      } else {
        delete (globalThis as { navigator?: unknown }).navigator;
      }
    }
  });

  console.log("\ncryptoKey tests passed");
}

await main();
