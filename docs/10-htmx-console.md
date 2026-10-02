# htmx console responsiveness

## Behavior

- Navigation uses bundled htmx. All authenticated screens share the same sidebar,
  active item, account navigation and logout placement.
- Dashboard, EC2 list and cost refreshes return only their selected HTML region.
  Direct links and browser history restoration still return full documents.
- Search runs locally without network requests. Delegated document listeners work
  after repeated navigation and row swaps. List refresh preserves search/state
  controls and updates the visible count and empty state.
- EC2 detail power actions return the detail region. Detail refresh queries only
  the authorized instance. DCV is enabled only in the running state.
- Transition rows/details poll after 1.5 seconds. Stable states stop polling.
  Hidden tabs pause polling and resume on visibility. Failed polls stop with an
  error notice; navigate/refresh to retry.
- htmx requests disable their action buttons and synchronize concurrent actions.
  HTTP/network/timeout failures appear as accessible plain text notices. Session
  expiry redirects the whole window to login, including during fragment requests.
- Runtime scripts/fonts remain local. Dynamic responses use no-store. htmx eval,
  injected scripts and browser history snapshots remain disabled. Default inline
  indicator styles are disabled for the existing CSP.
- Fonts retain Rounded M+ NM Regular/Bold and all glyphs. WOFF2 delivery reduces
  combined font bytes by 68.5%. Asset query versions invalidate old cached UI files.
- Focus outlines, hover states, semantic state colors and reduced-motion support
  improve keyboard navigation and visual feedback without large dependencies.

## Validation

```bash
go test -race ./...
bash tests/security.sh
# Install playwright@1.51.1 and its Chromium browser, then:
AWSPORTAL_BROWSER_TEST=1 go test -race ./cmd/awsportal -run TestBrowserConsole -v
```

The browser test runs the actual templates, headers, static resources and bundled
htmx against an httptest server. EC2 and Cost Explorer are simulated; AWS is not
modified. It checks repeated boosted navigation/search, list and cost fragment
refresh, detail power action and poll termination, idle traffic, failure notices,
external requests, document reload count and page overflow at 1366x768,
1920x1080, 2560x1440 and 3840x2160. CI uploads screenshots of the real rendered UI.

Responsiveness benefits are measured as fewer transferred bytes/document reloads,
not an unsupported claim that htmx makes AWS API calls faster. Live EC2/DCV/MFA and
real AWS network latency still require deployment verification.
