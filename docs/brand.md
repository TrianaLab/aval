# Brand

The mark is two stacked layers with a check on the top one: a result, stored once, reused after it is checked.

![Aval wordmark](assets/images/wordmark.svg#only-light)
![Aval wordmark](assets/images/wordmark-dark.svg#only-dark)

## Files

| File | Use |
|---|---|
| `assets/images/logo.svg`, `logo-dark.svg` | the mark on light and on dark backgrounds |
| `assets/images/wordmark.svg`, `wordmark-dark.svg` | mark and name on light and on dark backgrounds |
| `favicon.svg` | browser tab; switches colour with the system theme |
| `assets/images/social-preview.png` | 1280x640 social preview, rendered from `social-preview.svg` |
| `assets/images/favicon-16.png`, `favicon-32.png` | the favicon at 16 and 32 px |

The SVGs are the source. `make brand` renders the PNGs from them; commit both.

## Colours

| Token | Light | Dark |
|---|---|---|
| Mark (emerald) | `#059669` | `#34d399` |
| Links | `#047857` | `#34d399` |
| Wordmark (ink) | `#0f172a` | `#e2e8f0` |
| Background | `#ffffff` | `#0b1220` |

`docs/stylesheets/brand.css` holds these tokens for the site. Links use `#047857` on light because `#059669` is below 4.5:1 on white.

## Type

| Role | Font |
|---|---|
| Headings | Sora |
| Body | Manrope |
| Code and labels | JetBrains Mono |

All three are SIL OFL 1.1. The site serves them from `docs/assets/fonts/`, each with its licence; it loads no web fonts from a CDN.

## Clear space and size

- Keep clear space around the logo of at least half the mark's height.
- Do not show the mark below 16 px.
- The wordmark files already include their clear space in the `viewBox`.

## Do and don't

- Do draw on the 24x24 grid with 2 px strokes, round caps and round joins.
- Do use the dark variant on dark backgrounds.
- Don't recolour, stretch, rotate, outline or add effects to the mark.
- Don't set the name in a font; use the wordmark file.
