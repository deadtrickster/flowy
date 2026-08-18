import { chromium } from "playwright";
const [base, token, room = "general"] = process.argv.slice(2);
const browser = await chromium.launch();
const page = await browser.newPage({ viewport: { width: 1400, height: 900 } });
await page.addInitScript((t) => localStorage.setItem("flowy.token", t), token);
await page.goto(`${base}/chat/${room}`, { timeout: 25_000 }).catch(() => {});
await page.waitForSelector("main [data-body]", { timeout: 20_000 }).catch(() => {});
const find = () => {
  const el = document.querySelector("main [data-body]");
  let p = el?.parentElement;
  while (p && p.scrollHeight <= p.clientHeight) p = p.parentElement;
  return p;
};
for (let t = 0; t < 9000; t += 500) {
  const s = await page
    .evaluate(() => {
      const p = (() => {
        const el = document.querySelector("main [data-body]");
        let q = el?.parentElement;
        while (q && q.scrollHeight <= q.clientHeight) q = q.parentElement;
        return q;
      })();
      if (!p) return null;
      return {
        top: Math.round(p.scrollTop),
        h: p.scrollHeight,
        c: p.clientHeight,
        rows: document.querySelectorAll("main [data-body]").length,
      };
    })
    .catch(() => null);
  if (s)
    console.log(
      `${String(t).padStart(4)}ms  rows=${String(s.rows).padStart(4)}  scrollTop=${String(s.top).padStart(7)}  height=${s.h}  fromEnd=${s.h - s.top - s.c}`,
    );
  await page.waitForTimeout(500);
}
await browser.close();
