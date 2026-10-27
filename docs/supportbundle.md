# Support Bundles

A support bundle is a single `.tar.gz` holding the diagnostics needed to
troubleshoot an Antrea UI deployment: the backend's own logs and state, plus
diagnostics from plugins and from other services the operator configures. The
backend collects it on demand and keeps it on the Pod's filesystem (an
`emptyDir` volume) until it is downloaded and deleted, or until it expires.

Collection is extensible: every secondary source implements a small HTTP
protocol, shaped like a Kubernetes resource, described in [The source
protocol](#the-source-protocol).

Bundles are short-lived by design:

- they are lost when the Pod is replaced (e.g. by `helm upgrade`), since the
  `emptyDir` goes with it. They survive a restart of the backend container:
  each bundle's metadata is stored next to it, and reloaded at startup. A
  bundle that was still being collected is then reported as failed;
- they expire 6 hours after they are requested (`supportBundle.ttl`);
- at most 5 are retained (`supportBundle.maxBundles`), at most 2 may be
  collected at once (`supportBundle.maxConcurrent`, which a bundle deleted
  while collecting counts against until its collection has stopped), and all
  of them share a disk budget of 1GiB (`supportBundle.maxTotalBytes`).

The backend enforces the disk budget itself, on every write: a bundle that
would exceed it fails, rather than growing the volume until the kubelet evicts
the Pod. A bundle's bytes are released as soon as it is deleted or expires,
even if a download of it is still in progress: its file only leaves the disk
when that download ends. For a bundle still collecting, that is once its
collection has stopped, which can take up to 50s, while requests in flight to
its sources complete (see [The source protocol](#the-source-protocol)). The
`emptyDir` size limit, set slightly above the budget, is only a backstop. A
source tarball is briefly charged twice while it is copied into the bundle
tarball, so a new bundle is refused up front when the budget left is less than
twice `supportBundle.maxSourceBytes`, and `maxSourceBytes` may be at most half
of `maxTotalBytes`. That check only refuses bundles that clearly cannot fit: it
leaves out Antrea UI's own logs and every other source, so leave
`maxTotalBytes` room beyond twice `maxSourceBytes`.

## Authorization

Every route is authenticated like the rest of the API, then authorized with a
SelfSubjectAccessReview against the virtual resource `supportbundles` in the
`ui.antrea.io` API group. Nothing serves that resource: it only exists so that
access can be granted with ordinary RBAC. The verb matches the request:

| Request | Verb |
| --- | --- |
| `POST /api/v1/supportbundle` | `create` |
| `GET /api/v1/supportbundle` | `list` |
| `GET /api/v1/supportbundle/{id}`, `.../status`, `.../download` | `get` |
| `DELETE /api/v1/supportbundle/{id}` | `delete` |

The chart grants all four verbs in `antrea-ui-admin-core` (and therefore to the
admin-password mode). A caller without the grant gets a 403.

**This is admin-level access to diagnostics.** Bundles are collected as the
`antrea-ui-admin` ServiceAccount, whoever requests them (see [Trust
model](#trust-model)), so `create` lets the holder collect whatever every
configured source and every installed plugin's source returns to
`antrea-ui-admin`. **Bundles are also shared**: everyone holding the grant can
list, download and delete every bundle, whoever requested it. Only grant it to
users who may see all of these diagnostics.

## API

| Route | Behavior |
| --- | --- |
| `POST /api/v1/supportbundle` | Starts a collection. The body is optional: `{"since": "1h"}` (a Go duration) only collects rotated log files written within that window, and is forwarded to every source. Returns 202 with a `SupportBundle` body, `Location: /api/v1/supportbundle/{id}/status` and `Retry-After: 2`. Returns 429 when the hourly rate limit, `maxBundles` or `maxConcurrent` is reached, or when too little of the disk budget is left. |
| `GET /api/v1/supportbundle` | `{"items": [SupportBundle, ...]}`, oldest first. Since bundles are shared, this is how a client finds them again after a reload. |
| `GET /api/v1/supportbundle/{id}` | 303 to `/status`. |
| `GET /api/v1/supportbundle/{id}/status` | 200 with a `SupportBundle` body, or 404 for an unknown bundle. While collecting, also `Retry-After` and `Location` (the status itself). Once collected, `Location` points to the download. Never a redirect: `fetch()` would follow it and pull the whole tarball into the page's memory. |
| `GET /api/v1/supportbundle/{id}/download` | The tarball, as `application/gzip` with `Content-Disposition: attachment`. Supports `Range` requests. 404 while the bundle is still collecting, or if it failed. |
| `DELETE /api/v1/supportbundle/{id}` | Cancels the collection if it is in progress (sources get a best-effort `DELETE` too), and removes the bundle. |

When the feature is disabled (`supportBundle.enabled=false`), every route
returns 501.

A `SupportBundle`:

```json
{
  "id": "5f0c6c1e-2b8e-4a40-9c4e-0c1f1c1d2e3f",
  "status": "Collected",
  "createdBy": "alice",
  "createdAt": "2026-10-02T13:04:05Z",
  "expiresAt": "2026-10-02T19:04:05Z",
  "size": 123456,
  "sources": [
    {"name": "foo", "kind": "extra", "status": "Collected", "size": 2048},
    {"name": "my-plugin", "kind": "plugin", "status": "Failed", "error": "source returned HTTP 403: ..."}
  ]
}
```

`status` is one of `Collecting`, `Collected` and `Failed`, the same values as
Antrea's own SupportBundle API. A source that fails does not fail the bundle:
it is `Collected` with that source's error in `sources` (and in the tarball's
`manifest.json`). A bundle is `Failed` (with `error` set) only when Antrea UI's
own diagnostics or the tarball cannot be produced, when the disk budget is
exceeded, or when the backend restarted during the collection.

A collection does not depend on the session that requested it: logging out
does not stop it. It only stops when the bundle is deleted or expires, or when
the backend shuts down. Sources still running when it exceeds
`supportBundle.collectionTimeout` are given up on, and the bundle is delivered
without them.

## Tarball layout

```
manifest.json              # the SupportBundle above, plus the Antrea UI version
antrea-ui-backend/
  logs/                    # the current log file, and the rotated ones (or a
                           # README.txt, when file logging is off)
  version.txt
  config.yaml              # the loaded configuration, secrets redacted
  plugins.json             # the loaded plugin manifests
  goroutines.txt           # a goroutine dump
plugins/<name>.tar.gz      # one per plugin source, as returned by the source
extra/<name>.tar.gz        # one per extra source, as returned by the source
```

Source tarballs are stored exactly as returned, never extracted.

The backend logs come from a file the backend writes alongside stderr, in an
`emptyDir` volume, rotated at `backend.logs.maxSizeMB` with
`backend.logs.maxBackups` compressed files kept.

## Sources

There are two kinds of secondary sources.

**Plugin sources** are declared in a plugin's manifest (see
[plugins.md](plugins.md#the-manifest)):

```json
"supportBundle": {"apiServer": {"path": "/apis/foo.example.com/v1alpha1"}}
```

A plugin may only declare a source served through the Kubernetes apiserver,
typically by an APIService. `path` must be exactly `/apis/<group>/<version>`,
and Antrea UI appends `/supportbundles` to it, so a declaration can only ever
reach a `supportbundles` resource of an API group. A plugin cannot point Antrea
UI at an arbitrary URL. A `supportBundle` declaration this version of Antrea UI
does not recognize does not reject the plugin: the source is skipped, and the
bundle records it as an "unsupported source declaration". A declaration may
also set `maxConcurrent` (see [Concurrent
collections](#concurrent-collections)), e.g. `"supportBundle": {"apiServer":
{...}, "maxConcurrent": 1}`.

**Extra sources** are configured by the operator, in the Helm chart:

```yaml
supportBundle:
  extraSources:
    - name: foo                 # a DNS-1123 label, unique
      https:
        url: https://foo.foo-ns.svc/api/v1
        caData: |               # optional; the system trust store otherwise
          -----BEGIN CERTIFICATE-----
          ...
        serverName: ""          # optional
        insecureSkipVerify: false
    - name: bar
      apiServer:
        path: /apis/bar.example.com/v1alpha1
      maxConcurrent: 1          # optional; see "Concurrent collections"
```

Exactly one of `https` and `apiServer` must be set. `insecureSkipVerify` is
for development only, and logs a warning at startup. As for a plugin's source,
`antrea-ui-admin` must be granted access to an `apiServer` source, through an
aggregated ClusterRole (see
[plugins.md](plugins.md#support-bundle-sources)): otherwise every collection
from it fails with a 403.

### The source protocol

With `B` the source's base (the `https.url`, or the apiserver's address plus
`apiServer.path`), a source serves:

| Route | Behavior |
| --- | --- |
| `POST B/supportbundles` | Starts a collection. The body is a bundle with only `metadata.generateName` (always `antrea-ui-`) and `spec` set (`{"metadata": {"generateName": "antrea-ui-"}, "spec": {"since": "1h"}}`, or without `spec`). Returns 200, 201 or 202 with the bundle, whose `metadata.name` the source picks, optionally with `Retry-After`. |
| `GET B/supportbundles/{name}` | 200 with the bundle, whose `status.phase` is `Collecting`, `Collected` or `Failed` (with `status.message`). An empty or missing phase counts as `Collecting`. Optionally `Retry-After`. |
| `GET B/supportbundles/{name}/download` | 200 with the tarball. |
| `DELETE B/supportbundles/{name}` | Drops the bundle. 200, 204 or 404. |

A bundle has the shape of a Kubernetes object, so that an aggregated API can
serve it as a regular resource, with `download` as a subresource:

```json
{
  "metadata": {"name": "bundle-1"},
  "spec": {"since": "1h"},
  "status": {"phase": "Failed", "message": "disk full"}
}
```

`spec.since` is the `since` of the request to Antrea UI, forwarded unchanged
(a Go duration). Antrea UI ignores any other field, such as `apiVersion` and
`kind`, and sends neither. The `metadata.generateName` of the `POST` is there
for storage that requires a name or a `generateName`, as the generic
apiserver registry does; a source that picks the name itself can ignore it.
Antrea UI:

- requires the name to match `^[A-Za-z0-9._-]{1,128}$` (and not be `.` or
  `..`), and builds every URL from the configured base and that name. It never
  follows a redirect or a `Location` header. The name is otherwise opaque and
  need not be unique over time: a source with a `maxConcurrent` of 1 may return
  the same one for every bundle (see
  [Concurrent collections](#concurrent-collections));
- gives a source 30s to answer its `POST`, getting the token of an `https`
  source included (a slower answer fails the source), and waits for that
  answer even if the collection is canceled in the meantime, so that it learns
  the name of the bundle the source created and can `DELETE` it;
- honors `Retry-After` in seconds, clamped to [1s, 30s], and polls every 2s
  without one;
- keeps polling a bundle's status through a transport error (a response cut
  short included) or a 429, 502, 503 or 504, which can be a brief
  unavailability of the source or of the apiserver in front of it. Any other
  error fails the source;
- fails the source if its tarball exceeds `supportBundle.maxSourceBytes`
  (256MiB by default), or if the whole collection exceeds
  `supportBundle.collectionTimeout` (10 minutes by default). A `POST` in
  flight at that point, and the `DELETE` that follows it, still complete;
- calls `DELETE` as soon as the download is done, or when it gives up on the
  source. This is best effort (a backend restart skips it, for one), so a
  source should also expire the bundles it holds on its own;
- collects up to 4 sources in parallel.

A source authorizes each request itself. A 401 or 403 from a source only fails
that source.

### Concurrent collections

By default, Antrea UI may collect from a source for several bundles at once,
up to the global `supportBundle.maxConcurrent`. A source that can only hold so
many bundles at a time, for instance because it uses the same name for every
bundle, sets its own `maxConcurrent` (in the plugin manifest's `supportBundle`,
or on the `extraSources` entry); 0, the default, means no limit. A source that
can only hold one bundle sets 1.

Antrea UI then never has more than `maxConcurrent` collections outstanding
against the source, each from its `POST` until its `DELETE` has returned. A
collection that finds every slot taken waits for one, and its source stays
`Collecting` in the meantime; waiting collections are not served in order. The
wait counts against `supportBundle.collectionTimeout`: if it runs out, only
that source fails. A collection waiting for a source does not hold up the
bundle's other sources. It takes its slot of the source before one of its
bundle's 4 parallel slots, though, so while the bundle's other sources are
slow it may hold the slot for a while before contacting the source.

A collection reads the source's `maxConcurrent` when it takes its slot, not
when the bundle is requested. A limit changed in the meantime, for instance by
a plugin upgrade, therefore applies to it, and counts the collections still
running against the source, whatever limit they started under.

The limit is enforced by the Antrea UI backend (a single replica), on the
assumption that it is the source's only client. It does not guarantee that the
source holds no bundle when a new `POST` arrives, since `DELETE` is best
effort: a collection cut short by a backend restart, a `DELETE` that failed or
timed out (after 10s), or a `POST` that failed, timed out (after 30s) or got a
response Antrea UI could not use (an error status, an invalid body or name)
can leave one behind. A source that reuses a name must handle a `POST` that
arrives while the bundle it last created has not been deleted, whatever that
bundle's phase, by discarding that bundle and starting a new collection,
rather than failing the `POST`.

### How a source authenticates Antrea UI

Every request is made as the `antrea-ui-admin` ServiceAccount
(`system:serviceaccount:<release namespace>:antrea-ui-admin`), and says who
requested which bundle.

**`apiServer` sources** see the identity the aggregation layer forwards, as
for any aggregated API: the user `antrea-ui-admin`, which Antrea UI
impersonates, with two user extras:

- `supportbundle.ui.antrea.io/requested-by`: the username of the requester, as
  percent-encoded UTF-8;
- `supportbundle.ui.antrea.io/bundle-id`: the ID of the Antrea UI bundle.

The apiserver records them in its audit log, and the aggregation layer forwards
them as the `X-Remote-Extra-supportbundle.ui.antrea.io%2Frequested-by` and
`X-Remote-Extra-supportbundle.ui.antrea.io%2Fbundle-id` headers (the key is
percent-encoded, and its case may change on the way). An extension apiserver
built on the generic apiserver library only accepts them if the
`extension-apiserver-authentication` ConfigMap lists that prefix in
`requestheader-extra-headers-prefix`, which kubeadm and kind configure. No
credential reaches the source: the aggregation layer strips the
`Authorization` header.

**`https` sources** receive `Authorization: Bearer <token>`, a short-lived
token for `antrea-ui-admin` whose only audience is
`supportbundle.ui.antrea.io/<source name>`. The apiserver rejects such a token
for ordinary API calls, and a token minted for one source is rejected by every
other source. A source validates it with a TokenReview whose `spec.audiences`
is `["supportbundle.ui.antrea.io/<source name>"]`, checking that
`status.authenticated` is true and that `status.audiences` contains that
audience. It should then check that the reviewed identity may collect support
bundles, with a SubjectAccessReview for `create` on
`supportbundles.ui.antrea.io` passing the user, groups and extras the
TokenReview returned. The `system:auth-delegator` ClusterRole grants both
reviews.

Each request to an `https` source also carries:

- `X-Antrea-UI-Requested-By`: the username of the requester, as percent-encoded
  UTF-8 (usernames, OIDC claims in particular, may hold any character);
- `X-Antrea-UI-Support-Bundle`: the ID of the Antrea UI bundle.

These headers are assertions by Antrea UI. A source can trust them because the
request carries a valid `antrea-ui-admin` token for its audience, and exactly
as far as it trusts Antrea UI.

## Trust model

A support bundle's lifecycle is never tied to a user session. Collection is
asynchronous and fans out to secondary sources, so it runs under a service
identity, the `antrea-ui-admin` ServiceAccount. The requester's session only
authorizes the create request, against `supportbundles.ui.antrea.io` (see
[Authorization](#authorization)). After that, Antrea UI only keeps the
requester's username, as the bundle's `createdBy` and for the sources' audit
(see [How a source authenticates Antrea
UI](#how-a-source-authenticates-antrea-ui)). Antrea UI's own ServiceAccount
token is never sent to any source.

This design rests on three constraints:

1. **Granting `supportbundles.ui.antrea.io` means admin-level access to
   diagnostics.** Collection runs as `antrea-ui-admin`, which aggregates the
   RBAC of every installed plugin, so the grant reaches whatever each plugin's
   source chooses to return.
2. **A request cannot steer what the service identity touches.** The only
   input a request takes is `since`. Sources come from the operator's
   configuration (`extraSources`) and from installed plugins, never from the
   request. Plugins come from ConfigMaps in `plugins.namespace`, which is
   weaker trust than cluster-admin, but a plugin's source path is fixed to
   `/apis/<group>/<version>/supportbundles`, and `antrea-ui-admin` only holds
   what labeled ClusterRoles grant it (see
   [plugins.md](plugins.md#support-bundle-sources)). A future change that
   lets a request pick sources or destinations must also check the
   requester's own permissions, as Antrea does for the `authSecret` and
   `fileServer` of a `SupportBundleCollection`.
3. **Any Kubernetes data the backend collects in the future also runs as
   `antrea-ui-admin`,** limited to a fixed set defined in code.

The token an `https` source receives cannot be replayed against the apiserver
or another source, but the source learns who requests bundles, and what it
returns ends up in them. Minting those tokens takes a `serviceaccounts/token`
grant for `antrea-ui-admin`, which the chart always gives Antrea UI: RBAC
cannot limit the audience or lifetime of the tokens it allows, so a holder of
Antrea UI's credential could mint `antrea-ui-admin` tokens that outlive it
(deleting the `antrea-ui-admin` ServiceAccount revokes them). Only
configure sources you trust as much as Antrea UI itself, and keep
`insecureSkipVerify` off outside of development.

Antrea UI does not check the requester's RBAC for any source: granting
`supportbundles.ui.antrea.io` lets a user trigger a collection from every
source, and each source decides what it returns to `antrea-ui-admin`. Make
sure that everyone holding the grant may see what every source returns.
