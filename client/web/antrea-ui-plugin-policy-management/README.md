# @antrea/ui-plugin-policy-management

An Antrea UI plugin that adds a "Policy Management" section (NetworkPolicy
definitions and recommendations) to the shell app.

This package is structured like a [Headlamp plugin](https://headlamp.dev/docs/latest/development/plugins/getting-started):
a standalone package with its own `package.json`, whose entry point
(`src/index.ts`) exports a `register()` function that the host shell calls
with a host API object, rather than the shell statically importing this
feature's internals.

## Structure

```
src/
  index.ts                        # entry point: register(host)
  policy-management-page.ts/.html/.css
  policy-definitions-page.ts/.html
  policy-recommendations-page.ts/.html
  policy.service.ts
```

## Registration API

`register(host: AntreaPluginHost)` (from `@antrea/ui-plugin-sdk`) calls:

- `host.registerSidebarEntry(...)` — adds the "Policy Management" nav entry.
- `host.registerRoute(...)` — adds the `/policies` route tree.

The plugin never imports the host app's internal services (e.g. its
`AuthService`) directly; it reaches the K8s API proxy via
`host.getAuthToken()`, injected through the `ANTREA_PLUGIN_HOST` token.

## A note on Angular versions

Because this package is consumed as raw TypeScript source (like
`@antrea/ui-components`) rather than a pre-built Ivy library, its own
`@angular/*` devDependencies must resolve to the *exact* version the host app
(`antrea-ui-angular`) uses — Angular's compiler treats two different
`@angular/core` instances as structurally incompatible (`NG3004`) even when
resolved through a `link:`/`file:` dependency. If you bump Angular in the
host, bump the pinned exact versions here too.

## Status

Today the host consumes this package as a regular linked npm dependency,
resolved and compiled at build time — it is not yet dynamically loaded at
runtime from a separate bundle/volume the way Headlamp plugins are. That is
the intended next step (see the ANS add-on shared-volume plugin work), and
this package's boundary (own `package.json`, host-API-only coupling) is what
makes that follow-up possible without further refactoring.
