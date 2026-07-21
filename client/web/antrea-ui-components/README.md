# @antrea/ui-components

Framework-agnostic Lit web components implementing the Antrea UI pages
(Summary, Traceflow, Flow Visibility, Settings, Login) and shared building
blocks (`antrea-button`, `antrea-input`, `antrea-alert`, `antrea-card`,
`antrea-nav`). Consumed today by `antrea-ui` (React) and `antrea-ui-angular`
(Angular) — the same compiled components, hosted by either shell.

## Extension points

Downstream shells and plugins (e.g. `@antrea/ui-plugin-policy-management`)
sometimes need to hook into a page's data or inject content into it, without
this package needing to know that a specific plugin exists. The pattern to
follow, and the mistake to avoid:

**Expose the raw domain object, not a hand-picked projection of it.** The
first version of the `antrea-edge-selected` event (`antrea-flow-visibility-page.ts`)
shipped a curated `EdgeSelection` summary — `source`, `target`, `destPorts`,
`protected`. When the Policy Management plugin needed the raw policy *name*
(not the "name (Action)" display string already on the summary), the only fix
was adding `ingressPolicyNames`/`egressPolicyNames` fields to `EdgeSelection`
— a real change to this package, for one specific plugin's specific need.
That pattern doesn't scale: the next plugin with a different need means
another field, another release, another review cycle here.

The fix was giving the event access to the underlying `FlowEntry[]` records
the selection was built from, alongside the couple of cheap, generically
useful fields that predated any plugin (`protected`, `destPorts`). A plugin
that wants something not already on the summary — byte counts, a label,
whatever — reads it straight off the raw entries. This package doesn't need
to change again for that class of request.

This is the same shape Headlamp's own extension points use: its map
extension gives plugins the raw `kubeObject` (the actual underlying resource)
plus an open-ended `detailsComponent` slot, rather than a summary Headlamp's
core would have to keep extending. A shared contract type at the extension
point boundary is unavoidable (Headlamp has `Node`/`Edge`; we have
`EdgeSelection`) — the property worth protecting is that the type has an
escape hatch to raw data and doesn't need a new field every time a new
consumer shows up.

**House style for new extension points in this package:**

1. **"Here's what got selected/happened" events** carry the raw domain
   object(s) (already-exported library types) plus at most a couple of
   cheap, stable fields that predate any specific plugin's ask — never a
   field added *because* one plugin needed exactly that projection.
2. **"Render extra content here" needs** use a named `<slot>` (see
   `edge-extra` on the flow-visibility page's details card) — the component
   owns the mount point and the data event; it never needs to know what gets
   rendered into the slot, or by whom.
3. **New nav destinations / whole new pages** don't belong here at all —
   that's `registerRoute`/`registerSidebarEntry` at the
   `@antrea/ui-plugin-sdk` / host level, which already requires zero changes
   to this package.
4. When a plugin need doesn't fit an existing raw-data handle, ask: does the
   page already compute this internally and just isn't exposing it (→ expose
   the raw data — cheap), or does the plugin need something the page never
   computes at all (→ a real, judged addition, decided case by case — not
   every plugin need is this package's job to satisfy generically).

Don't add speculative slots/events to a page ahead of an actual plugin need —
that's designing for hypotheticals. Apply this when a real need shows up, not
before.
