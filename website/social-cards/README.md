# Article social cards

One 1200×630 preview image per article, in `website/static/cards/<slug>.png`,
referenced from the article's front matter as `images: ["cards/<slug>.png"]`.

`card.html` holds the design and one entry per article: the title and one
sentence taken word for word from the article. To add a card, add the entry,
then render:

```bash
cd website/social-cards
python3 -m http.server 8765 --bind 127.0.0.1 &
npm i playwright && npx playwright install chromium
node shoot.mjs <slug>
cp out/<slug>.png ../static/cards/
```

On a Linux release Playwright does not recognise yet, prefix the install and
render with `PLAYWRIGHT_HOST_PLATFORM_OVERRIDE=ubuntu24.04-x64`. Look at every
card before committing it.
