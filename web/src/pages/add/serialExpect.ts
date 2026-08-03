/**
 * Expect/send helper over a live Web Serial capture buffer.
 * Callers push inbound text via append(); scripts await expect() / send().
 *
 * expectAny picks the RIGHTMOST match among patterns in the post-mark slice:
 * a stale « Password: » earlier must not win over a later « Username: », or the
 * script types the password into the username field.
 */

export type SerialSession = {
  append: (text: string) => void;
  getCapture: () => string;
  /** Text received since the last mark() — ignore stale pre-reboot prompts. */
  getSinceMark: () => string;
  clearCapture: () => void;
  send: (text: string) => Promise<void>;
  /** Write bytes AS-IS (no CR appended) — interactive TTY keystrokes. */
  sendRaw: (text: string) => Promise<void>;
  expect: (pattern: RegExp, timeoutMs: number) => Promise<string>;
  expectAny: (patterns: RegExp[], timeoutMs: number) => Promise<{ index: number; match: string }>;
  sleep: (ms: number) => Promise<void>;
  /** Discard buffered text so the next expect only sees new data. */
  mark: () => void;
};

export type SerialSessionOpts = {
  write: (bytes: Uint8Array) => Promise<void>;
  onCapture?: (full: string) => void;
  /** Max chars kept in the rolling capture (default 200_000). */
  maxCapture?: number;
};

/** Exported for unit tests — prefer the rightmost hit among patterns. */
export function rightmostMatch(
  slice: string,
  patterns: RegExp[],
): { index: number; match: string } | null {
  var bestIdx = -1;
  var bestPos = -1;
  var bestMatch = "";
  var pi = 0;
  for (pi = 0; pi < patterns.length; pi++) {
    var flags = patterns[pi].flags.includes("g")
      ? patterns[pi].flags
      : `${patterns[pi].flags}g`;
    var r = new RegExp(patterns[pi].source, flags);
    var m: RegExpExecArray | null;
    while ((m = r.exec(slice)) !== null) {
      if (m.index >= bestPos) {
        bestPos = m.index;
        bestIdx = pi;
        bestMatch = m[0];
      }
    }
  }
  if (bestIdx < 0) return null;
  return { index: bestIdx, match: bestMatch };
}

/**
 * Wrap a session so every ACTION (send/sendRaw/expect/sleep) first waits on
 * `gate`. Used by « pause provisioning »: the script freezes before its next
 * step while the operator drives the console by hand through the inner
 * session. Reads (capture, marks) pass through untouched.
 *
 * An expect already in flight when the pause lands keeps running — the pause
 * takes effect at the NEXT step, and the UI says so.
 */
export function createPausableSession(
  inner: SerialSession,
  gate: () => Promise<void>,
): SerialSession {
  return {
    append: (text) => inner.append(text),
    getCapture: () => inner.getCapture(),
    getSinceMark: () => inner.getSinceMark(),
    clearCapture: () => inner.clearCapture(),
    mark: () => inner.mark(),
    async send(text: string) {
      await gate();
      await inner.send(text);
    },
    async sendRaw(text: string) {
      await gate();
      await inner.sendRaw(text);
    },
    async expect(pattern: RegExp, timeoutMs: number) {
      await gate();
      return inner.expect(pattern, timeoutMs);
    },
    async expectAny(patterns: RegExp[], timeoutMs: number) {
      await gate();
      return inner.expectAny(patterns, timeoutMs);
    },
    async sleep(ms: number) {
      await gate();
      return inner.sleep(ms);
    },
  };
}

export function createSerialSession(opts: SerialSessionOpts): SerialSession {
  var maxCapture = opts.maxCapture ?? 200000;
  var capture = "";
  var markAt = 0;
  var waiters: {
    patterns: RegExp[];
    resolve: (v: { index: number; match: string }) => void;
    reject: (e: Error) => void;
    timer: ReturnType<typeof setTimeout>;
  }[] = [];

  var notify = () => {
    var slice = capture.slice(markAt);
    var i = 0;
    while (i < waiters.length) {
      var w = waiters[i];
      var hit = rightmostMatch(slice, w.patterns);
      if (hit) {
        clearTimeout(w.timer);
        waiters.splice(i, 1);
        w.resolve(hit);
      } else {
        i++;
      }
    }
  };

  return {
    append(text: string) {
      capture = (capture + text).slice(-maxCapture);
      if (markAt > capture.length) markAt = 0;
      opts.onCapture?.(capture);
      notify();
    },
    getCapture() {
      return capture;
    },
    getSinceMark() {
      return capture.slice(markAt);
    },
    clearCapture() {
      capture = "";
      markAt = 0;
      opts.onCapture?.(capture);
    },
    mark() {
      markAt = capture.length;
    },
    async send(text: string) {
      // Console CLIs (Cisco-like / FS S3900) expect CR only — same as PuTTY.
      // CRLF is read as Enter + a second empty Enter: username submit then an
      // empty password (or password + garbage), so login fails while PuTTY works.
      await opts.write(new TextEncoder().encode(text + "\r"));
    },
    async sendRaw(text: string) {
      await opts.write(new TextEncoder().encode(text));
    },
    expect(pattern: RegExp, timeoutMs: number) {
      return this.expectAny([pattern], timeoutMs).then((r) => r.match);
    },
    expectAny(patterns: RegExp[], timeoutMs: number) {
      return new Promise((resolve, reject) => {
        var immediate = rightmostMatch(capture.slice(markAt), patterns);
        if (immediate) {
          resolve(immediate);
          return;
        }
        var timer = setTimeout(() => {
          var idx = waiters.indexOf(entry);
          if (idx >= 0) waiters.splice(idx, 1);
          reject(
            new Error(
              `serial expect timeout after ${timeoutMs}ms (wanted ${patterns
                .map((p) => p.source)
                .join(" | ")})`,
            ),
          );
        }, timeoutMs);
        var entry = { patterns, resolve, reject, timer };
        waiters.push(entry);
      });
    },
    sleep(ms: number) {
      return new Promise((resolve) => setTimeout(resolve, ms));
    },
  };
}
