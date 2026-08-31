/**
 * THE URL A DEV SERVER JUST PRINTED, PICKED OUT OF THE OUTPUT.
 *
 * 01M1558DPM1HRGZNJGMVW24DHF item 6. Start vite or next in the panel and it
 * prints a loopback URL you cannot click - the terminal draws text, and the
 * thing a person wants next is that page.
 *
 * WHOSE LOOPBACK IT IS, WHICH IS THE ONLY HARD PART HERE. "127.0.0.1:5173"
 * printed by a shell ON THIS HOST is reachable from the browser: same machine.
 * The identical string printed inside a microVM is the GUEST's loopback and
 * resolves, from the browser, to the person's own machine - so the link either
 * fails or, much worse, opens something else of theirs that happens to be on
 * that port. A link that quietly goes somewhere else is worse than no link, so
 * a guest URL is reported with reachable:false and the panel says what it is
 * rather than offering to open it.
 *
 * 0.0.0.0 IS NOT AN ADDRESS TO VISIT. Servers print it to mean "every
 * interface", and a browser asked for http://0.0.0.0 does something different
 * per platform. It is rewritten to 127.0.0.1, which is what the person meant
 * and what every one of those servers is in fact listening on.
 *
 * SCANNING IS INCREMENTAL BECAUSE THE OUTPUT IS. A write lands whenever the pty
 * flushed, so a URL is routinely split across two of them - and the banner is
 * colourised, so escape sequences sit between the parts. Both are handled by
 * keeping a tail of what was seen and stripping escapes before matching, the
 * same shape as the ANSI scrubber on the node side.
 */

// Enough to hold a long URL plus the escapes woven through a colourised banner,
// bounded so a long-running shell cannot grow this without limit. A URL longer
// than this is one nobody was going to click.
const CARRY = 512;

// CSI, OSC and the short two-byte escapes, removed before matching. A
// colourised banner puts these BETWEEN the scheme and the host, so matching
// without stripping them finds nothing on exactly the servers this is for.
// biome-ignore lint/suspicious/noControlCharactersInRegex: the ESC byte is the thing being matched - an escape sequence is control characters, that is its definition
const ANSI = /\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)|\x1b\[[0-?]*[ -/]*[@-~]|\x1b[@-Z\\-_]/g;

// A loopback URL and nothing else. Deliberately not any URL: a link to
// somewhere on the internet is not what a dev server just started, and offering
// to open whatever host appears in the output is a way to turn a log line into
// a click on somebody else's site.
const LOOPBACK =
  /https?:\/\/(localhost|127\.0\.0\.1|0\.0\.0\.0|\[::1\])(:\d{1,5})?(\/[^\s"'<>)\]]*)?/gi;

export type DevUrl = {
  /** What to open, after 0.0.0.0 is rewritten. */
  url: string;
  /** Whether THIS browser can reach it - false for a guest's own loopback. */
  reachable: boolean;
};

/**
 * Rewrite the addresses that are not meant to be visited.
 *
 * 0.0.0.0 means "listening on every interface" and is not a destination; ::1
 * and localhost are left exactly as printed, because they already resolve and
 * rewriting them would show the person a URL they did not see in the output.
 */
function visitable(url: string): string {
  return url.replace(/^(https?:\/\/)0\.0\.0\.0/i, "$1127.0.0.1");
}

/**
 * Watch a stream of terminal output for a dev server's URL.
 *
 * `where` is the panel's own: "host" means the shell shares this machine with
 * the browser, anything else means a guest, whose loopback is not ours.
 */
export function devUrlScanner(where: string) {
  let carry = "";
  const decoder = new TextDecoder();
  const reachable = where === "host";

  return {
    /**
     * Feed one write. Returns the newest URL in it, or null.
     *
     * The NEWEST rather than the first: a dev server prints Local and Network
     * lines together, and restarting it prints a fresh banner. The last one
     * seen is the one that is currently true.
     */
    push(bytes: Uint8Array): DevUrl | null {
      // stream: true, so a multi-byte character split across two writes is not
      // decoded into a replacement character that could sit inside a URL.
      const text = (carry + decoder.decode(bytes, { stream: true })).replace(ANSI, "");

      let last: string | null = null;
      let after = 0;
      LOOPBACK.lastIndex = 0;
      for (const m of text.matchAll(LOOPBACK)) {
        last = m[0];
        after = (m.index ?? 0) + m[0].length;
      }

      // CARRY WHAT WAS NOT MATCHED, not simply the tail. Keeping the tail
      // wholesale would leave a URL that was already reported sitting in the
      // buffer, and every later write would rediscover and re-report it - so a
      // server that had been stopped would keep offering its address for as
      // long as anything else printed. Everything up to the end of the last
      // match is spent; what follows may be the start of the next one.
      carry = text.slice(Math.max(after, text.length - CARRY));
      if (!last) return null;

      // A trailing dot or comma is the sentence's, not the URL's - servers
      // print "ready at http://localhost:5173/." often enough to matter.
      const cleaned = last.replace(/[.,;:]+$/, "");
      return { url: visitable(cleaned), reachable };
    },
  };
}
