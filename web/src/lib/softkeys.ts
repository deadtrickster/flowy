/**
 * A SOFT KEYBOARD'S KEYS, WHICH THE TERMINAL OTHERWISE NEVER SEES.
 *
 * 01M1558DPM1HRGZNJGMVW24DHF item 5. The row called this "contenteditable plus
 * beforeinput". Read at the source, most of that is already done and the real
 * gap is narrower and worse.
 *
 * WHAT GHOSTTY-WEB ALREADY DOES. Terminal.open() sets contenteditable="true",
 * tabindex, role and aria on the element, and registers its own beforeinput
 * listener whose entire body is preventDefault() - so the element never
 * actually mutates. Composition is wired end to end: compositionstart sets a
 * flag, and compositionend calls onDataCallback(event.data), which is the path
 * to the pty. An Android keyboard typing ordinary words fires composition
 * events and ALREADY WORKS. None of that needs replacing.
 *
 * WHAT IS BROKEN. handleKeyDown returns immediately on keyCode === 229, the
 * placeholder soft keyboards send, and Android sends 229 for keys that are not
 * composition at all. Backspace is the one that matters: keydown arrives as
 * 229 and is dropped, beforeinput arrives as deleteContentBackward and is
 * preventDefaulted, and nothing whatever reaches the shell. Enter is the same
 * shape, and so is swipe or autocorrect text, which arrives as insertText with
 * no composition around it.
 *
 * So this listens for the beforeinput events ghostty declines to act on and
 * sends the bytes itself. preventDefault does not stop propagation, so its
 * listener and this one both run whatever order they were added in.
 *
 * THE DOUBLE-SEND IS THE WHOLE DIFFICULTY, and it is a desktop regression
 * rather than a phone one - which makes it the dangerous half. On a physical
 * keyboard ghostty's keydown handler already encoded and sent the character,
 * AND the browser then fires beforeinput insertText for the same keystroke. An
 * unguarded listener here types everything twice for every user on a real
 * keyboard while fixing phones.
 *
 * The discriminator is the keydown that came first. A real key produces a
 * keydown with a real keyCode, which ghostty acted on; a soft key produces 229
 * or nothing at all, which it dropped. So a beforeinput is ours only if no
 * real keydown just preceded it.
 *
 * WHY A TIME WINDOW AND NOT A FLAG CLEARED ON THE NEXT EVENT. Plenty of keys
 * produce a keydown and NO beforeinput - arrows, function keys, a bare
 * modifier. A flag set by keydown and cleared only by the next beforeinput
 * would be left standing by those, and would then swallow the next genuine
 * soft-keyboard event instead. The window expires on its own, so a keydown
 * that is never followed by anything costs nothing.
 */

// The gap between a physical keydown and the beforeinput it causes, which the
// browser dispatches in the same user-input turn. 50ms is far longer than that
// and far shorter than a person's next keystroke, so it separates "the same
// keystroke, already sent" from "a new one nobody sent" with room on both
// sides.
const SAME_KEYSTROKE_MS = 50;

/**
 * What to send for one beforeinput, or "" for one that is not ours to send.
 *
 * insertCompositionText is deliberately absent: it fires for every intermediate
 * state of a composition, and compositionend already sends the finished text.
 * Acting on it would send each half-typed form as well as the result.
 */
function bytesFor(inputType: string, data: string | null): string {
  switch (inputType) {
    case "insertText":
      return data ?? "";
    // Both spellings reach the shell as a carriage return. A pty is what turns
    // that into a newline, per its own termios - sending "\n" here would be
    // this panel deciding that on the shell's behalf.
    case "insertLineBreak":
    case "insertParagraph":
      return "\r";
    // DEL, not BS. This is what a terminal's erase character is on every system
    // this console talks to, and it is what ghostty's own keydown path sends
    // for Backspace - so a phone and a keyboard produce the same byte.
    case "deleteContentBackward":
      return "\x7f";
    case "deleteContentForward":
      return "\x1b[3~";
    default:
      return "";
  }
}

/**
 * Send the keys a soft keyboard produces that the emulator drops.
 *
 * Returns the detach function, like attachMouseReporting - the panel rebuilds
 * its terminal on every run, and a listener left on a discarded element is a
 * second sender for the next session.
 */
export function attachSoftKeyboard(
  el: HTMLElement,
  send: (data: string) => void,
  now: () => number = () => performance.now(),
): () => void {
  let lastRealKey = Number.NEGATIVE_INFINITY;

  const onKeyDown = (e: KeyboardEvent) => {
    // isComposing and 229 are the two ways a keydown says "this is not a real
    // key, the IME is mid-word" - and they are exactly the two ghostty's own
    // handler returns early on. Anything else it acted upon.
    if (e.keyCode === 229 || e.isComposing) return;
    lastRealKey = now();
  };

  const onBeforeInput = (e: InputEvent) => {
    // Already sent by the keydown path. See the note above: this is the
    // desktop double-send, not an edge case.
    if (now() - lastRealKey < SAME_KEYSTROKE_MS) return;
    const data = bytesFor(e.inputType, e.data);
    if (data) send(data);
  };

  el.addEventListener("keydown", onKeyDown);
  el.addEventListener("beforeinput", onBeforeInput as EventListener);
  return () => {
    el.removeEventListener("keydown", onKeyDown);
    el.removeEventListener("beforeinput", onBeforeInput as EventListener);
  };
}
