/**
 * A check that WRITES must not be pointed at a live node by accident.
 *
 * These scripts seed fixtures - messages, todos, assignments - to have
 * something to assert against. Against the gate's throwaway node that is the
 * whole point. Pointed at the dogfood node by hand it writes into the store
 * everybody is standing in, and the log is APPEND-ONLY AND SIGNED, so there is
 * no tidying up afterwards. That happened: 236 permanent rows, 54 of them into
 * #general, the room the operator reads, from verifying a browser fix the
 * fastest way rather than the right way.
 *
 * Defaulting a fixture room helped and was not enough - the rows still land in
 * the real store. So the guard is on the NODE, not the room.
 *
 * The escape hatch is deliberately awkward to type and says what it does, and
 * it warns even when honoured, because the case for using it is always "just
 * this once".
 *
 * The right answer when you need a real browser against real code is a firecode
 * VM: it brings its own node and its own postgres, so it is as real as the live
 * one and nobody is standing in it.
 */

const LOOPBACK = new Set(["localhost", "127.0.0.1", "::1", "[::1]", "0.0.0.0"]);

/**
 * refuseRemote exits unless `base` is a loopback address. `what` names the
 * calling check, so the refusal says which script stopped and why.
 */
export function refuseRemote(base, what) {
  let host;
  try {
    host = new URL(base).hostname;
  } catch {
    console.error(`${what}: "${base}" is not a URL`);
    process.exit(2);
  }
  if (LOOPBACK.has(host)) return;

  if (process.env.FLOWY_CHECK_ALLOW_REMOTE === "1") {
    console.error(
      `${what}: WRITING TO A NON-LOOPBACK NODE (${host}) because FLOWY_CHECK_ALLOW_REMOTE=1.
  Every row this seeds is permanent - the log is append-only and signed.`,
    );
    return;
  }

  console.error(
    `${what}: refusing to write to ${host} - this check SEEDS FIXTURES and that node is not loopback.
  Every message it posts would be a permanent signed row in a store other people are using.
  Run the gate in a firecode VM instead: it has its own node and its own postgres, so it is
  a real browser against real code with nobody standing in it.
  If you genuinely mean it, set FLOWY_CHECK_ALLOW_REMOTE=1.`,
  );
  process.exit(1);
}
