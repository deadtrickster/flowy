// Both controls exist and both DO something, asserted on the node rather than
// on the screen: a button that renders and changes nothing is the failure this
// whole row is about.
import { chromium } from "playwright";

const [base, token] = process.argv.slice(2);
const bearer = { Authorization: `Bearer ${token}` };
const die = (m) => {
  console.error(m);
  process.exit(1);
};
const rooms = async () =>
  (await (await fetch(`${base}/api/rooms`, { headers: bearer })).json()).rooms ?? [];

const browser = await chromium.launch();
try {
  const page = await browser.newPage();
  await page.addInitScript((t) => localStorage.setItem("flowy.token", t), token);

  const made = `made-by-the-console-${Date.now()}`;
  page.once("dialog", (d) => d.accept(made));
  await page.goto(`${base}/chat/general`, { waitUntil: "domcontentloaded" });
  const create = page.locator("[data-room-create]");
  await create.waitFor({ timeout: 15000 });
  await create.click();
  await page.waitForURL(`**/chat/${made}`, { timeout: 15000 });

  // THE NODE, not the url. Navigating proves the click ran; only the roster
  // proves a room exists.
  if (!(await rooms()).some((r) => r.name === made)) {
    die(`the console navigated to ${made} and the node has no such room`);
  }

  // AND LEAVING IT. The room was just created by this principal, so they are a
  // member and `left` must be true - a false here would mean the button called
  // the door about somebody else.
  page.once("dialog", (d) => d.accept());
  const leave = page.locator("[data-room-leave]");
  await leave.waitFor({ timeout: 15000 });
  await leave.click();
  const said = page.locator("[data-room-left]");
  await said.waitFor({ timeout: 15000 });
  const text = (await said.textContent())?.trim() ?? "";
  if (!text.startsWith("left #")) {
    die(`leaving a room this principal owns said ${JSON.stringify(text)}`);
  }
  const after = (await rooms()).find((r) => r.name === made);
  if (after && (after.role ?? "") !== "") {
    die(`the node still gives this principal role ${JSON.stringify(after.role)} in ${made}`);
  }
  console.log(`created ${made} from the console and left it: ${text}`);
} finally {
  await browser.close();
}
