# Rounded M+ 1mn font asset

AWSPortal uses `rounded-mplus-1mn-regular.ttf` as its bundled UI font.

Source package: Rounded M+ (M+ OUTLINE FONTS derivative), distribution historically mirrored by OSDN/Homebrew font casks.

The production build must contain:

`cmd/awsportal/web/fonts/rounded-mplus-1mn-regular.ttf`

No CDN, Google Fonts, or other runtime font download is permitted.

The font binary is intentionally not substituted with a different typeface. CI must fail when the required asset is absent. Keep the upstream license notice alongside the binary when importing the asset.
