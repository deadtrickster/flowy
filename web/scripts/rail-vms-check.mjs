// THE RAIL ENTRY TAKES YOU THERE.
//
// The source walk says /vms is in the nav. It cannot say that pressing it
// works: a NavLink to a path the router does not serve renders happily and
// lands on the catch-all, which is a page that exists and is not this one.
//
// So this is the click, and the assertion is the PAGE, not the URL. A router
// that changed the address and drew nothing would pass a URL check.

import { chromium } from "playwright";

const [base, operator] = process.argv.slice(2);
const die = (why) => {
  console.error(why);
  process.exit(1);
};
if (!base || !operator) die("usage: rail-vms-check.mjs BASE OPERATOR_TOKEN");

const browser = await chromium.launch();
try {
  const page = await browser.newPage({ viewport: { width: 1400, height: 1000 } });
  await page.addInitScript((t) => localStorage.setItem("flowy.token", t), operator);
  await page.goto(`${base}/`, { timeout: 30_000 });

  // SCOPED TO THE RAIL, and this is not fussiness. The first version looked
  // for a[href="/vms"] anywhere on the page and passed with the NAV LINK
  // POINTING SOMEWHERE ELSE - it had found the home page's link, which has
  // always been there. A check that cannot fail for its own case, caught by
  // proving the red rather than by reading it.
  const entry = page.locator('[data-nav] a[href="/vms"]');
  const found = await entry.count();
  if (found === 0) {
    die(`the nav offers no way to the shells page. The home page links it, so a check that did
not scope to [data-nav] would pass right here.`);
  }
  if (found > 1) {
    die(`the nav has ${found} entries for /vms, so this is asserting whichever came first`);
  }
  await entry.click();
  await page.waitForURL((url) => url.pathname === "/vms", { timeout: 15_000 }).catch(() => {});

  // THE PAGE, NOT THE ADDRESS. api_vm.go answers 503 on a host with no
  // firecode, so the panel may say it cannot run VMs - that is this page
  // speaking, and it is the arrival that is being asserted.
  const panel = page.locator("[data-vm-panel]");
  await panel.waitFor({ state: "visible", timeout: 20_000 }).catch(() => {});
  if ((await panel.count()) === 0) {
    die(`pressing the nav entry left ${page.url()} without the shells panel on it - the entry
points somewhere the router does not draw this page`);
  }
  const state = await panel.getAttribute("data-vm-state");

  // AND THE ENTRY CALLS IT WHAT THE PAGE CALLS ITSELF. The rail said "shells"
  // while the page's h1 said "VMs", the route said /vms and the refusal copy
  // said "spawning a VM" - three surfaces agreeing and one not. A link whose
  // name is not the name of where it lands makes a reader doubt they arrived,
  // and it is the kind of drift no test notices, because both words are true
  // of something on the page.
  const label = ((await entry.textContent()) ?? "").trim().toLowerCase();
  const heading = ((await page.locator("[data-vm-panel] h1").first().textContent()) ?? "")
    .trim()
    .toLowerCase();
  if (!heading) {
    die("the vms panel drew no h1, so there is nothing for the rail entry to agree with");
  }
  if (label !== heading) {
    die(
      `the rail calls it ${JSON.stringify(label)} and the page calls itself ${JSON.stringify(
        heading,
      )}. One of them has to move - a link should land somewhere that shares its name.`,
    );
  }

  console.log(
    `the nav entry opened /vms and the panel drew, in state ${JSON.stringify(state)}, ` +
      `under the same name the rail gives it (${JSON.stringify(heading)})`,
  );
} finally {
  await browser.close();
}
