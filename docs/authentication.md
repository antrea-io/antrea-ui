# Authentication and Authorization

Antrea UI supports four ways of logging in. Except for the admin password, all
of them make the backend act as *your own* Kubernetes identity: what you can see
and do in the UI is whatever your Kubernetes RBAC allows — with one exception,
the flow visibility data, described in [Flow data is not yet
per-user](#flow-data-is-not-yet-per-user).

The browser never holds a Kubernetes credential and never talks to the
kube-apiserver directly. Every API request goes to the Antrea UI backend, which
holds the credential server-side and presents it upstream on your behalf — the
"backend for frontend" pattern Antrea UI has always used.

## The four modes

| Mode | Helm value | Identity presented to Kubernetes |
| --- | --- | --- |
| Admin password | `auth.basic.enable` (default `true`) | The `antrea-ui-admin` ServiceAccount, via impersonation |
| OIDC | `auth.oidc.enable` (default `false`) | Your own, from the id_token |
| Kubeconfig upload | `auth.kubeconfig.enable` (default `false`) | Your own, from the kubeconfig's current context |
| Kubernetes token | `auth.token.enable` (default `true`) | Your own, from the pasted token |

The modes are independent — enable any combination. The login page renders only
the controls for the modes that are enabled. At least one of them must be on.

`auth.bearerToken.enable` (default `true`) is a fifth flag but not a login mode:
it authenticates individual API requests rather than creating a session. See
[`Authorization: Bearer`](#authorization-bearer-not-a-login-mode) below.

The kubeconfig upload and Kubernetes token modes validate the credential
against the API server at login with a `SelfSubjectReview`
(`authentication.k8s.io/v1`), which requires **Kubernetes >= 1.28**. This is
also enforced by the chart's `kubeVersion`.

### Admin password

The historical mode, and the only one where the user has no Kubernetes identity
of their own. Every admin-password session is impersonated as the
`antrea-ui-admin` ServiceAccount, so all such users share exactly the same
cluster access: whatever the aggregated `antrea-ui-admin` ClusterRole grants.

This is also the only mode that can change the admin password (Settings →
Password). A user who logged in with their own Kubernetes identity cannot: that
password is Antrea UI's own credential, with no Kubernetes RBAC behind it.

### OIDC

The user authenticates against an OIDC provider, and the resulting id_token is
what Antrea UI presents to the kube-apiserver.

**This only works if the kube-apiserver trusts the same issuer.** It must be
started with `--oidc-issuer-url` and `--oidc-client-id` matching the provider and
client that Antrea UI is configured with. Without that, login will succeed and
then every API call will fail with 401. See [oidc.md](oidc.md).

Antrea UI requests the `openid`, `email`, `groups` and `offline_access` scopes by
default (`auth.oidc.scopes`). `offline_access` is what makes the provider issue a
refresh token; without one, the session ends as soon as the id_token expires,
which is often only a few minutes. `groups` populates the group claims that
group-based RBAC needs. `email` is requested because `--oidc-username-claim=email`
is by far the most common kube-apiserver configuration, and a provider omits the
claim entirely if the scope was not asked for.

Some providers reject scopes that are not defined on the application rather than
ignoring them, so if your provider returns `invalid_scope` at login, trim
`auth.oidc.scopes` to what it accepts.

### Kubeconfig upload

The user pastes or uploads a kubeconfig. The backend reads the current context's
credential and discards everything else immediately; only the credential itself
is retained, in memory, for the life of the session.

Supported credentials are a bearer `token`, or `client-certificate-data` plus
`client-key-data`. Rejected, with an explanation on the login form:

- `exec` credential plugins and `auth-provider` entries. These describe a program
  to run, or a provider to contact, on *your* machine. Running them inside the
  Antrea UI Pod would at best fail and at worst execute a user-supplied command
  there.
- References to files (`client-certificate`, `client-key`, `tokenFile`). Those
  paths only exist on your machine. Use
  `kubectl config view --raw --minify` to produce a self-contained kubeconfig.
- HTTP basic authentication.

A certificate that has already expired is rejected at login rather than failing
on the first page load.

### Kubernetes token

The user pastes a bearer token, typically from
`kubectl create token <serviceaccount>`. The token is validated against the API
server (with a `SelfSubjectReview`) before the session is created, so a bad paste
fails on the login form.

### `Authorization: Bearer` (not a login mode)

`auth.bearerToken.enable` (default `true`) is separate from the login mode
above, even though both accept the same credential. It lets a non-browser client
(a script, a controller, the e2e tests) call the Antrea UI API with an
`Authorization: Bearer <token>` header instead of holding a session cookie. Such
a request creates no session; the identity lasts exactly as long as the request.

The two are independent flags because they are different exposures: the login
mode is a page a human uses, while this is an authentication path on every API
route, taken by clients that are not browsers and so are not covered by the
cross-origin gate. The browser UI never sends this header, so a deployment whose
only client is the UI can turn it off at no cost.

A bearer request has no login step, so its token is validated on the request
itself, the same way a login validates one. This cannot be left to the upstream
call to catch: `GET /auth/session` and the flow stream resolve an identity and
then never talk to Kubernetes, so on those routes there is no upstream to reject
a bad token. Validating up front also keeps Antrea UI from being an
unauthenticated way to test Kubernetes credentials against an API server the
caller may not be able to reach directly.

A successful validation is cached for a minute, keyed on a hash of the token, so
a client making many calls costs one `SelfSubjectReview` per minute rather than
one per request. The cost of that cache is the same minute of delay before a
revoked token stops being accepted. Validations that miss the cache are
rate-limited per client IP, since those are the ones that reach the API server.

Once the API server has accepted a token, its `exp` claim is a checked fact
rather than an assertion by the caller, so it is honoured from that point on: a
cache entry never outlives the token's own expiry, and a long-running request —
the flow stream — is closed when the credential behind it expires. Without that
the stream would run until the client disconnected, since it never presents the
credential to anything again. An opaque token (a legacy ServiceAccount token,
which is not a JWT) carries no expiry claim, so there is nothing to enforce and
such a stream is bounded only by the client going away.

## RBAC an administrator must grant

In the OIDC, kubeconfig and token modes, users reach the API server as
themselves, so their own RBAC decides what the UI can show them. Without a
binding, they log in successfully and then get 403s everywhere.

**Grant each group of users a ClusterRole (or namespace-scoped Role) that you
write yourself, scoped to what that group should actually be allowed to do.**
The chart's own roles are described below so you can use them as a starting
point, but neither is intended as the thing you bind end users to.

### The two ClusterRoles the chart ships

`antrea-ui-admin-core` carries the permissions the UI itself needs, and nothing
else:

- `get` on `antreacontrollerinfos`
- `list` and `get` on `antreaagentinfos`
- `get`/`list`/`watch`/`create`/`delete` on `traceflows` and
  `traceflows/status`
- `get` on the `/featuregates` non-resource URL

Its rule list is static: it only ever changes when you upgrade the chart, and
you can read exactly what it grants in
`build/charts/antrea-ui/templates/clusterroles.yaml`. It is a reasonable model
for a role of your own that gives a group the whole UI and nothing more.

`antrea-ui-admin` is an [aggregated
ClusterRole](https://kubernetes.io/docs/reference/access-authn-authz/rbac/#aggregated-clusterroles).
Its rules are assembled by the API server from every ClusterRole labeled
`rbac.ui.antrea.io/aggregate-to-antrea-ui-admin: "true"` —
`antrea-ui-admin-core` plus whatever any installed plugin contributes (see
[plugins.md](plugins.md)). It is what the admin-password mode impersonates,
which is why that mode sees everything the UI and its plugins can reach.

**Its contents grow on their own.** Deploying a plugin that ships an aggregated
ClusterRole silently widens `antrea-ui-admin`, and therefore silently widens
every binding to it. Bind it to a user or group only if you mean "this user
gets whatever the UI and its plugins can ever do, including what a plugin
installed next month adds".

### Flow data is not yet per-user

The flow visibility stream (`GET /api/v1/flows/stream`) is the one part of the
UI that per-user RBAC does **not** cover. The backend subscribes to the Flow
Aggregator over its own mTLS gRPC connection, and nothing consults the caller's
Kubernetes permissions, so any user who can log in can see every flow the Flow
Aggregator exports — including a user whose RBAC grants them nothing else, who
will get 403s on every other page.

Authorization for this endpoint is being implemented upstream in
[antrea-io/antrea#8221](https://github.com/antrea-io/antrea/pull/8221). Until
that lands, treat "can log in at all" as the access-control boundary for flow
data: if that is too broad for your deployment, disable the integration with
`flowAggregator.enabled=false`, or restrict which modes can be used to log in.

### The plugin trade-off

The flip side: a user bound to `antrea-ui-admin-core`, or to your own role
modeled on it, does *not* pick up a plugin's permissions, so that plugin's
pages will 403 for them. Plugin permissions have to be granted to those users
deliberately — either by adding the plugin's rules to your own role, or by
binding the plugin's own ClusterRole alongside it. That is the intended
trade-off: aggregation is a convenience for the single admin identity, not a
grant mechanism for end users.

### Example

```bash
# A role of your own, scoped deliberately. Start from antrea-ui-admin-core's
# rules and remove what this group should not have — but keep its `list` on
# `namespaces`, or the namespace filters will come up empty (see below).
kubectl create clusterrolebinding antrea-ui-network-operators \
  --clusterrole=your-antrea-ui-viewer --group=network-operators
```

Granting less is fine and expected — a user bound only to a namespace-scoped
subset simply sees less. The UI hides what a partially-authorized user cannot
do, described next, rather than letting them hit a page and get a 403.

## What the frontend knows about your permissions

`GET /api/v1/access-summary` (optional `?namespace=<ns>`) tells the frontend
what the logged-in user is allowed to do, so it can hide navigation entries and
routes it would otherwise only get a 403 from. It runs four requests against
the API server as the caller's own identity — a `SelfSubjectReview`, a
`SelfSubjectRulesReview`, and two `SelfSubjectAccessReview`s — the same APIs
[Kubernetes documents for this exact
purpose](https://kubernetes.io/docs/reference/access-authn-authz/authorization/#checking-api-access).
The response looks like:

```json
{
  "username": "system:serviceaccount:rbac-test-alpha:sa-ns-edit",
  "groups": ["system:serviceaccounts", "system:serviceaccounts:rbac-test-alpha", "system:authenticated"],
  "clusterAdmin": false,
  "rules": {
    "resourceRules": [{"verbs": ["get"], "apiGroups": ["crd.antrea.io"], "resources": ["antreaagentinfos"]}],
    "nonResourceRules": [{"verbs": ["get"], "nonResourceURLs": ["/featuregates"]}],
    "incomplete": false
  },
  "namespaces": ["rbac-test-alpha", "rbac-test-beta"]
}
```

**This is a rendering hint, never an authorization decision.** Every real
request the UI makes is still authorized by the API server exactly as before;
a wrong answer here costs a spurious 403 (or a spuriously hidden button) and
nothing more.

There is no partial answer. A `200` means every field is authoritative;
anything else means the frontend shows everything, exactly as it did before
this endpoint existed. Successful responses are cached for the session, failed
ones are not, so the next page to ask for the summary re-requests it rather
than reusing a failure.

- `rules` is the raw `SelfSubjectRulesReview` result. Rules are additive: a
  rule appearing there means the user definitely has that permission, but a
  rule *not* appearing only means "not allowed" when `rules.incomplete` is
  `false`. When `incomplete` is `true` — the API server saying its rule list is
  not exhaustive, e.g. because a webhook authorizer is in play and cannot be
  enumerated — the frontend fails open and shows everything, exactly like
  today's pre-access-summary behaviour.
- `clusterAdmin` is computed from a `SelfSubjectAccessReview` for `*` verb, `*`
  group, `*` resource, which is true for any wildcard ClusterRole (not only one
  literally named `cluster-admin`).
- `namespaces` is `["*"]` when the user can `list` namespaces cluster-wide.
  Otherwise it is derived from a cluster-wide watch on RoleBindings, kept
  in-memory by antrea-ui using its own ServiceAccount: the namespaces where a
  RoleBinding names the user, or one of their groups, as a subject. Kubernetes
  has no self-service API for "which namespaces can I see", so this is a
  heuristic and may under-report; it is never used to block access. This is the
  only privileged read antrea-ui performs on the user's behalf, and it is why
  the `antrea-ui` ClusterRole (antrea-ui's own operations role, not
  `antrea-ui-admin`) grants `list`/`watch` on
  `rolebindings.rbac.authorization.k8s.io` cluster-wide.

  The scan sees RoleBindings only, so it cannot see a **ClusterRoleBinding**: a
  user granted namespaced access cluster-wide is the subject of no RoleBinding
  and would be reported with no accessible namespaces at all. This is why
  `antrea-ui-admin-core` grants `list` on `namespaces` — it takes the `["*"]`
  branch above and never reaches the scan. Keep that rule in any role you model
  on it.

  An empty list means "you are the subject of no RoleBinding, and you cannot
  list namespaces", which is a real answer. When antrea-ui cannot answer at all — the RoleBinding cache has not
  synced yet, or the `rolebindings` grant above was never applied — the endpoint
  returns **503** rather than an empty list, since the two would otherwise be
  indistinguishable. Static-admin sessions are unaffected: their answer never
  comes from the resolver, so a broken resolver cannot lock an operator out of
  the one login used to fix cluster RBAC.

### The cluster-scope sentinel namespace

`SelfSubjectRulesReview` always requires a namespace, and its response mixes
cluster-scoped rules and that namespace's rules into one flat list with
nothing marking which is which. Evaluating it against `kube-system` — the
obvious choice — would misreport a user whose only RoleBinding happens to live
in `kube-system` as having cluster-wide rights.

Instead antrea-ui evaluates cluster-scoped queries against a namespace that
holds no RoleBindings at all: **`antrea-ui-cluster-scope-probe`**. It does not
need to exist as a Kubernetes object — `SelfSubjectRulesReview` never looks it
up, it only needs to be a non-empty string — but if an administrator creates
it and adds a RoleBinding there, that RoleBinding's rules would silently leak
into every user's cluster-scoped result.

**Administrators should not create RoleBindings in
`antrea-ui-cluster-scope-probe`.** antrea-ui detects it if they do — the same
RoleBinding watch behind `namespaces` above also watches this one namespace —
and reacts by marking `rules.incomplete: true` for cluster-scoped queries
(namespaced `?namespace=` queries are unaffected, since the probe is irrelevant
to them) until the offending RoleBinding is removed. The UI then falls back to
showing everything, exactly as if the endpoint were unavailable. This is
logged once, at error level, on the backend, naming the offending
RoleBinding(s), and it recovers automatically with no antrea-ui restart. The
blast radius of that fallback is cosmetic, not a security boundary: the API
server still authorizes every real request regardless of what
`access-summary` reports.

## Sessions

Logging in creates a session on the backend, keyed by an opaque random ID in the
`antrea-ui-session` cookie (`HttpOnly`, `SameSite=Strict`, and `Secure` when
`security.cookieSecure` is set). The credential itself stays in the backend's
memory:

- It is never written to a Secret, a ConfigMap, or a volume.
- It is never logged, at any verbosity, and never included in an error message.
- It is overwritten in place when the session ends.

The third guarantee is specific to sessions, and is worth reading precisely. A
request authenticated with `Authorization: Bearer` creates no session, so there
is nothing to overwrite and nothing that tries to: its token lives in the
request headers, in Go strings that cannot be mutated, and is left to the
garbage collector when the request ends. Zeroing antrea-ui's own copy would not
change that, for the same reason the session path documents an exception for
client-go's bearer round-tripper. The one place a bearer token would otherwise
outlive its request is the validation cache, and that stores a SHA-256 of the
token rather than the token itself.

A session ends when any of these happens:

| Trigger | Default | Helm value |
| --- | --- | --- |
| No request for a while | 30 minutes | `session.idleTimeout` |
| Absolute age | 12 hours | `session.maxLifetime` |
| The credential expires and cannot be renewed | — | — |
| Logout, in this tab or another | — | — |
| The same user logs in from too many places | 10 concurrent | `session.maxSessionsPerUser` |
| The backend Pod restarts | — | — |

While a browser tab is visible, the UI pings `GET /auth/session` every five
minutes, so "idle" means "no open visible tab" rather than "no clicks". The
trade-off is that an unattended but visible tab holds its session for the full
12 hours.

An attached flow visibility stream is the one exception: it keeps its session
alive whether or not the tab is in the foreground, because a flow page is
something people background on purpose and expect to still be collecting when
they come back. This applies only while the flow page is the open route —
navigating to another page closes the stream, after which the ordinary
visible-tab rule applies again. The absolute cap still bounds such a session,
and so does its credential.

For OIDC sessions the backend renews the id_token with the refresh token shortly
before it expires, so the session can outlive the token, up to the absolute cap.
Renewal happens on whatever request arrives inside that window, which for a
backgrounded flow page is the stream itself — anything that extends a session
also renews it, so a session can never outlive the credential behind it.

A renewal that fails is not immediately fatal. It is attempted a minute before
the credential expires, so the session still holds a working credential and the
attempt is simply retried; an identity provider that is briefly unreachable
costs nothing. If renewal is still failing when the credential does expire — a
refresh token revoked at the provider, say — the session ends there.

The store holds at most `session.maxSessions` sessions (default 1000), and
at most `session.maxSessionsPerUser` of those for any one identity (default
10). The per-user cap is what makes the global one safe: without it, one caller
scripting logins fills the store and every other user is refused a session until
the attacker's sessions idle out. Exceeding the per-user cap does not fail the
login — it evicts that user's own least-recently-used session, so the pressure
stays on the identity causing it. If the store is globally full and nothing in it
has expired, further logins get a 503 until something frees up.

Admin-password sessions are exempt from the per-user cap. Every one of them
authenticates as the same literal `admin`, so applying the cap would give
everyone sharing that password a single budget between them: the 11th browser to
log in would silently sign out whoever logged in first. There is no per-user
identity there to bound, and nothing to bound it against — the cap exists to stop
one identity from starving the others, and a mode where every session is the same
identity has no others to starve. Those sessions are still subject to the global
cap and to the login rate limiter.

**A Pod restart logs everyone out.** For OIDC that is invisible (the provider
signs the user straight back in); for the kubeconfig and token modes the user has
to paste their credential again. The Deployment is single-replica for the same
reason: a second replica would not recognize sessions created by the first.

### Cross-site request forgery

A cookie is attached by the browser automatically, including on requests a page
on some other origin made. Two independent things stop that from being useful to
an attacker:

- `SameSite=Strict` on the session cookie, so the browser does not send it at
  all on a cross-site request. This is the primary defence.
- A server-side origin check, for browsers or embeddings where `SameSite` does
  not behave as expected. A request is accepted when `Sec-Fetch-Site` is
  `same-origin` or `none`, or — for a client that sends no `Sec-Fetch-Site` —
  when `Origin` matches the deployment's own (`url`, falling back to the
  request's `Host`). A request with neither header is a non-browser client
  (`curl`, a controller), which has no cookie jar to abuse and is accepted.

The check covers every route that acts on a session, and also the routes that
create or destroy one: without it on `/auth/login/token`, another origin could
POST a credential of its choosing and leave the victim's browser holding a
session for the *attacker's* Kubernetes identity. Logout additionally accepts a
cross-site top-level navigation, since the UI performs it with
`window.location`; the silent forms (`<img>`, an iframe, a `fetch()`) are still
refused.

A `redirect_url` given to `/auth/logout` or `/auth/oauth2/login` is only
followed if it stays on antrea-ui's own origin. Anything else is ignored, so
neither endpoint can be used as an open redirect.

Requests that authenticate with `Authorization: Bearer` instead of a cookie are
exempt: a browser cannot attach that header cross-origin without the server
opting in through a CORS preflight, which antrea-ui does not.

In development mode (`APP_ENV=dev`) the check also accepts
`http://localhost:3000`, and the cookie is set with `SameSite=Lax`, because the
Vite dev server and the backend are necessarily on different origins. Never run
a production deployment with `APP_ENV=dev`.

## What to expect when upgrading

Deployments that relied on "log in as admin, see everything" keep working
unchanged as long as `auth.basic.enable` stays `true`. Enabling any other mode
without granting those users RBAC of their own (see [RBAC an administrator must
grant](#rbac-an-administrator-must-grant)) will produce users who can log in but
cannot see anything. This is the intended behaviour change: the point of the
other modes is that Kubernetes, not Antrea UI, decides what each user can do.

Two other changes worth noting:

- The Kubernetes API proxy no longer applies its own path allowlist. RBAC is the
  only guard, which is what it already effectively was.
- The `antrea-ui-jwt-key` Secret is gone, along with the Pod annotation that
  forced a restart whenever it was regenerated.
