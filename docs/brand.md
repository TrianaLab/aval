# Brand

![Aval wordmark](assets/images/wordmark.svg)

The mark is a check inside a loop arrow: a result that is verified, then reused.

## Files

| File | Use |
|---|---|
| [`assets/images/logo.svg`](assets/images/logo.svg) | mark, light backgrounds |
| [`assets/images/logo-dark.svg`](assets/images/logo-dark.svg) | mark, dark backgrounds |
| [`assets/images/wordmark.svg`](assets/images/wordmark.svg) | mark and name, light backgrounds |
| [`assets/images/wordmark-dark.svg`](assets/images/wordmark-dark.svg) | mark and name, dark backgrounds |
| [`favicon.svg`](favicon.svg) | browser tab; [`favicon.png`](assets/images/favicon.png) is the 32 px fallback |
| [`assets/images/social-preview.svg`](assets/images/social-preview.svg) | 1280x640 card; [`social-preview.png`](assets/images/social-preview.png) is the upload |
| [`stylesheets/brand.css`](stylesheets/brand.css) | palette tokens for the docs site |

The SVGs are written by hand on a 24 px grid with a 2 px round stroke. Edit the SVG, then run `make brand` (needs `rsvg-convert` from librsvg) to render the PNGs again.

## Colours

| Token | Hex | Use |
|---|---|---|
| `--aval-teal` | `#0d9488` | mark on light, favicon tile |
| `--aval-teal-light` | `#2dd4bf` | mark and links on dark |
| `--aval-teal-deep` | `#0f766e` | links on light |
| `--aval-ink` | `#0b1220` | dark background |

## Clear space and size

- Keep empty space around the mark of at least a quarter of its height.
- The smallest size is 16 px. Below 24 px, use the favicon tile.

## Do and don't

- Do use the dark variant on dark backgrounds.
- Do keep the mark and the name in the wordmark together, as drawn.
- Don't recolour, stretch, rotate or outline the mark.
- Don't add effects such as shadows or gradients.
