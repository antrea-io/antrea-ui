# Namespace scope in the Antrea UI frontend

These rules apply to every page that can show content for a Namespace, called a
**Namespace-aware page**: Overview, Flow Visibility, plugin pages, and any page added later.

## Principle

Verdicts and the Namespace list are hints: they shape what the UI offers, picks and requests,
but they never grant anything, and the API decides what the user gets. The UI uses verdicts in
what it offers and in what it picks by itself: it leaves out what is known to be denied, offers
what is uncertain, and picks All Namespaces by itself only when it is known to be allowed. It
uses a scope in the URL as given, never checks a Namespace before selection, and never refuses a
selected scope: a refusal comes from the API. The rules below apply this, and where one of them
is more specific, it wins.

## Definitions

- **Scope.** A page shows content for a scope: either **All Namespaces** or **one Namespace**.
  Selecting several Namespaces at once is not supported.
- **The Namespace list.** The Namespaces the UI offers the user. When the user **may list
  Namespaces** (the access summary says so), it is every Namespace in the cluster. Otherwise it
  is the Namespaces where a RoleBinding names the user or one of their groups. It is a hint and
  can be wrong in both directions: a RoleBinding may grant nothing a given page uses, and access
  granted by something other than RBAC (e.g. a webhook authorizer) is not reflected. It is in
  alphabetical order. It is **cut** when listing stopped at the safeguard of rule 2,
  **unknown** when it could not be loaded (the access summary or the listing failed), and
  **complete** otherwise, even when empty.
- **Verdicts.** For each permission, the access summary gives a verdict, either cluster-wide or
  in one Namespace:
  - *allowed*: a rule grants it, or the user may do every verb on every resource (the access
    summary says so);
  - *denied*: neither, and the rules are complete;
  - *unknown*: neither, but the rules are incomplete (e.g. a webhook authorizer is in
    use), or they could not be fetched.
- **A page's verdict.** Each page has a set of permissions (the data it shows). Its verdict is
  *allowed* if any of them is *allowed*, *denied* if all of them are *denied*, and *unknown*
  otherwise. Unless stated otherwise, "the page's verdict" is the cluster-wide one.
- **Preferences.** Stars, the default scope and the last scope chosen (rule 12) belong to the
  signed-in user. They are never shown to another user of the same browser.

## 1. Page availability

A page is available, both in the navigation and through a direct link, if the page's verdict
is *allowed* or *unknown*, the user may list Namespaces, or the Namespace list is not empty. A
scope in the URL does not make an unavailable page available.

## 2. The picker

The picker offers the Namespace list. The list is never narrowed by checking a Namespace's
permissions, and whether the rules are complete does not affect it. It reflects the user's
access when they signed in or last reloaded the UI; a reload refreshes it.

The list is not capped: a user who may list Namespaces gets every Namespace in the cluster. As
a safeguard only, listing stops at 10,000 Namespaces, and the list is then cut (rule 8).

With nothing typed, the picker shows All Namespaces first (when rule 4 offers it), then the
user's starred Namespaces, or the first 10 Namespaces of the list if they have none.

Typing searches the whole list, starred Namespaces included, by name prefix, ignoring case and
surrounding spaces. The picker shows at most 10 matches, says when there are more, and the user
narrows the search by typing more. All Namespaces is hidden while typing. The search only
filters the list: a name that matches nothing can be selected only under rule 8.

## 3. Stars

A user stars up to 10 Namespaces, picked from the list, or typed where rule 8 allows typing.
Stars are shared by every page. The user stars and unstars a Namespace in any picker, on a page
or in Settings; an 11th star is refused, with a message giving the limit. Stars are not checked,
and are shown in alphabetical order. A starred Namespace that later drops out of the list stays
starred, and is shown, searched and selected like any other star until the user unstars it.

Stars and the default scope (rule 5) are independent: the default Namespace need not be
starred, setting it does not star it, and unstarring it does not change the default.

## 4. All Namespaces

All Namespaces is offered unless the page's verdict is *denied*: it is offered when the verdict
is *allowed* or *unknown*.

## 5. Default scope

A page decides its scope when it opens, and again whenever its URL changes (rule 12), using
the first of these that applies:

1. **The scope in the page URL** (a reload, a shared link, Back or Forward, a link from another
   page, or the navigation entry, which carries the last scope chosen). It is used as given, as
   soon as the page is available (rule 1), without waiting for the Namespace list, and with no
   fallback, even when it is All Namespaces and the page's verdict is *denied*. An invalid scope
   (a value that is neither `_all` nor a valid Namespace name, an empty value, `namespace` given
   more than once) is reported, and nothing is selected.
2. **The user's default scope**, set in Settings. There is one default for the whole UI, picked
   with the same picker as a page (rules 2 and 8). Settings offers All Namespaces only to a user
   who may list Namespaces, or when the access summary could not be fetched (rule 9). A default
   of All Namespaces is skipped on a page whose verdict is *denied*, and the page moves to the
   next step. A default Namespace is always used, even if the user has lost access to it since.
3. **All Namespaces**, if the page's verdict is *allowed*, not when it is *unknown*.
4. **The first starred Namespace that is still in the list**, or the first starred Namespace if
   the list is empty, unknown or cut (the cases where stars can be typed); or
   else the first Namespace of the list.
5. **Nothing selected**, with an explanation and a way to name a Namespace (rule 8).

Steps 2 to 5 wait until the Namespace list and the verdicts are known, or have failed to load.
A scope chosen by steps 2 to 5 is not written to the URL (rule 12).

## 6. After selection

No Namespace is checked before the user selects it, and none is hidden or marked because of such
a check. Once a scope is selected, the page loads its content for that scope and shows what the
API allows. A page made of several parts, such as Overview, may leave out a part whose verdict
in the selected scope is *denied*: it makes no requests for that part, and says that something
is hidden. When that leaves out every part, it says that nothing on the page is available to the
user in that scope, and the scope and the picker stay as they are. Every part that is not left
out makes its requests, and a refusal comes from the API.

For example, Flow Visibility always opens a stream for the scope and reports a refusal; Overview
looks at the verdicts for the selected Namespace and leaves out the parts they deny.

When the selected Namespace is not in a complete, non-empty list, the page still loads it, and
adds a note: "Namespace X does not exist" for a user who may list Namespaces, once a fresh check
confirms it (it may have been created since the list was loaded), or "Namespace X is not in your
list: it may not exist, or you may not have access" otherwise. There is no such note when the
list is empty, unknown or cut.

## 7. The picker shows the selected scope

The selected scope always appears in the picker as selected, even if the picker would not
otherwise offer it: a scope from the URL, a default Namespace, a typed name, a star that left
the list, or All Namespaces on a *denied* page.

## 8. Naming a Namespace

When the Namespace list is empty, unknown or cut (rule 2), the user can type a Namespace name.
A typed name is treated like any other selection. A name that is not a valid Namespace name is
rejected.

## 9. Failure

If the access summary cannot be fetched, the page still works: All Namespaces is offered, the
user can name a Namespace, and the API's answer decides what is shown.

## 10. Changes during a session

The selected scope is never changed or removed automatically, even if the user's permissions
change. A scope that is no longer accessible is reported by the page when it loads it
(rule 6).

The Namespace list and the verdicts can become out of date during a session, and a reload
refreshes them.

## 11. Plugin pages

A plugin page is Namespace-aware when it declares so, and then follows these rules, including
the URL parameters of rule 12, like any other page. It may replace parts of them, such as its
picker or how it picks its scope; a scope it sets through Antrea UI is still kept in its URL and
becomes the last scope chosen, like any other. Its verdict comes from the permissions it
declares; when it declares none, it is *allowed* for a user who may do every verb on every
resource (the access summary says so), and *unknown* otherwise. A plugin page that declares
nothing is not Namespace-aware.

## 12. The scope in the URL

Every Namespace-aware page keeps the scope the user chose in its URL, as `?namespace=<name>`, or
`?namespace=_all` for All Namespaces (`_all` cannot be a Namespace name), so a reload or a
shared link keeps it. `namespace` in a URL always means the scope: no page, plugin pages
included, uses it for anything else. Only an explicit choice is written: a selection in the
picker, a typed name, or a link followed. Writing it updates the URL in place, without adding a
history entry.

A page follows its URL: when the URL changes (Back, Forward, or switching between the page's
own views), the page takes its scope from the new URL, or from the default order when there is
none. Links between a page's own views keep the scope. A link inside a page may carry a scope to
another Namespace-aware page, which treats it like any scope in its URL.

The UI remembers **the last scope chosen**: the last valid scope a Namespace-aware page took
from its picker or its URL (a selection, a typed name, a link followed, a shared link opened, a
reload, Back or Forward). A scope chosen by steps 2 to 5 of rule 5 does not count. There is one
for the whole UI, kept for the browser tab: a reload keeps it, a new tab starts without one, and
signing out and back in, in the same tab, keeps it. Changing the default scope in Settings does
not change it. The navigation entries of the Namespace-aware pages carry it in their links, so
the page receives it as a scope in its URL (rule 5, step 1), and opening that link in a new tab
carries it too. When there is none, the navigation entries carry no scope, and the page starts
from the default order. While the user is on a page, the entries for that page's own views carry
the scope in its URL, or none, instead, so switching views keeps a scope the user chose. The
redirect from the UI's root to a landing page carries the last scope chosen like a navigation
entry, and a redirect between a page's own views keeps the scope in its URL.
