# antrea-ui-angular

A downstream Angular + [Clarity Design System](https://clarity.design/) shell
for Antrea UI. It reuses the same page-level Lit web components as the
default React frontend ([antrea-ui](../antrea-ui)) — Summary, Traceflow, Flow
Visibility, Settings, and Login all come from
[`antrea-ui-components`](../antrea-ui-components), unmodified. This app only
provides the Angular/Clarity chrome around them: header, vertical nav,
routing, and auth-token state.

## Architecture

- `src/app/core/auth.service.ts` — signal-based auth state (`token`),
  `sessionExpired()`, and `logout()`. Mirrors the React app's Redux store.
- `src/app/pages/*.ts` — thin standalone components that place a Lit page
  element in the template, bind `[token]`, and forward
  `(antrea-session-expired)` to `AuthService.sessionExpired()`.
- `src/app/pages/login-page.ts` — hosts `<antrea-login-page>` and listens for
  the `antrea-token` event to populate `AuthService`.
- `src/app/layout/header`, `src/app/layout/nav` — Clarity `clr-header` /
  `clr-vertical-nav` chrome, standalone components.
- `src/styles.css` — imports Clarity's CSS and antrea-ui-components' design
  tokens (`tokens.css`), then overrides the `--antrea-color-*` custom
  properties to match Clarity's light theme so the Lit components blend in.

Since the Lit components are plain custom elements, every component that
places one in its template needs `schemas: [CUSTOM_ELEMENTS_SCHEMA]` so
Angular doesn't complain about unknown properties/attributes.

## Consuming antrea-ui-components

`antrea-ui` (React) compiles `antrea-ui-components`' TypeScript source
directly via Vite. Angular's stricter TS program does not tolerate that
source as-is (missing `override` modifiers, decorator `tslib` helpers,
etc.), so this app instead consumes the library's built bundle:

```ts
import '@antrea/ui-components/dist';
```

Run `npm run build` in `antrea-ui-components` to (re)generate `dist/index.js`
before building this app locally. The Docker build
(`build/frontend-angular.dockerfile`) does this automatically.

## Development

```bash
yarn install
yarn start          # ng serve, proxies /api and /auth to localhost:8080 (see proxy.conf.json)
```

## Building

```bash
yarn build          # outputs to dist/antrea-ui-angular/browser
```

or via Docker/Make from the repo root:

```bash
make build-frontend-angular
```
