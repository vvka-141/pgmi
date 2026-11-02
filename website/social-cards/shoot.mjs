import { chromium } from 'playwright';
const slugs = process.argv.slice(2);
const b = await chromium.launch();
const p = await b.newPage({ viewport: { width: 1200, height: 630 }, deviceScaleFactor: 1 });
for (const s of slugs) {
  await p.goto(`http://127.0.0.1:8765/card.html#${s}`);
  await p.reload();
  await p.waitForLoadState('networkidle');
  const over = await p.evaluate(() => { const c = document.querySelector('.foot').getBoundingClientRect(); const cl = document.getElementById('claim').getBoundingClientRect(); const t=document.getElementById('title').getBoundingClientRect(); const m=document.querySelector('.mark').getBoundingClientRect(); return {claimBottom: cl.bottom, footTop: c.top, titleRight: t.right, markLeft: m.left, titleTop: t.top, markBottom: m.bottom}; });
  await p.screenshot({ path: `out/${s}.png` });
  console.log(s, JSON.stringify(over));
}
await b.close();
