# Rounded M+ 1mn font asset

AWSPortal uses `rounded-mplus-1mn-regular.ttf` as its bundled UI font.

Source package: Rounded M+ (M+ OUTLINE FONTS derivative), distribution historically mirrored by OSDN/Homebrew font casks.

The production build must contain:

`cmd/awsportal/web/fonts/rounded-mplus-1mn-regular.ttf`

No CDN, Google Fonts, or other runtime font download is permitted.

The font binary is intentionally not substituted with a different typeface. CI must fail when the required asset is absent. Keep the upstream license notice alongside the binary when importing the asset.

## Browser delivery

The browser uses `rounded-mplus-1mn-regular.woff2` and
`rounded-mplus-1mn-bold.woff2`. These are losslessly packaged from the bundled
TTF originals. No glyph subsetting is applied. All Unicode mappings and the
400/700 weights are preserved. Combined font transfer falls from 6,524,272 to
2,055,452 bytes (68.5% reduction before HTTP compression).

To regenerate with fontTools and brotli installed:

```python
from fontTools.ttLib import TTFont
from pathlib import Path
for path in Path('cmd/awsportal/web/fonts').glob('*.ttf'):
    font = TTFont(path)
    font.flavor = 'woff2'
    font.save(path.with_suffix('.woff2'))
```
