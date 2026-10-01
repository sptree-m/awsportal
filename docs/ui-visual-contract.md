# AWSPortal UI Visual Contract

## Canonical visual reference
The canonical reference is the approved Operations Console mockup generated on 2026-10-01 in the project conversation. Preserve its visual language on every UI change: dark navy persistent sidebar, compact white workspace, dense fixed-width typography, restrained blue primary actions, explicit green/red/orange state indicators, compact tables, thin borders, and high information density.

The source PNG must be retained with the project release/design assets when binary upload is available. This document is the CI-testable contract and must not be replaced by a different visual direction.

## Typography
- Rounded M+ NM is the product typeface.
- Font assets are self-hosted; no runtime font CDN or external font request.
- Dense operations UI is designed around fixed-width glyphs.
- Tables/logs/IDs/IP/timestamps must preserve predictable alignment.

## Resolution contract
- 1366x768: all primary operations usable.
- 1920x1080: reference density.
- 2560x1440 and 3840x2160: increase visible information, do not simply enlarge cards.
- No raster UI chrome. Prefer CSS and local SVG.

## Network-weight contract
"Lightweight" means low network transfer. Client CPU/GPU rendering is acceptable.
- No external runtime UI/font/chart/icon resources.
- No React/Vue/Bootstrap/Tailwind/jQuery/Chart.js or CDN.
- HTML is SSR.
- CSS/JS/font/static assets must be cacheable with long-lived caching when fingerprinted.
- Do not use screenshots/background photography as application chrome.
- Charts use CSS/SVG/Canvas generated client-side from compact data.
- Poll only while state is transitioning; stable pages must not continuously transfer EC2 state.
- Logs use bounded/paginated incremental retrieval; never download whole log files for display.
- APIs return only fields needed by the current view.

## Mandatory review for every UI PR
1. Compare against the canonical mockup and this contract.
2. Check 1366x768, 1920x1080, 2560x1440, 3840x2160.
3. Verify table density, sidebar consistency, typography, state colors + text, alignment and whitespace.
4. Run external-resource detection.
5. Review transferred bytes for initial load and recurring idle traffic.
6. Reject visual regressions or unnecessary network traffic even when functionality passes.

## Principle
Use local compute to create a rich interface; do not buy visual richness with network traffic.
