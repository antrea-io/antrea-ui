# SSO Authentication Using OIDC

The default authentication method for Antrea UI uses an admin password. However,
we also support SSO integration using the OIDC protocol. When SSO is enabled, it
is possible to disable password-based authentication altogether.

There are 2 main methods to enable OIDC support in Antrea UI:

1. Use the built-in Dex support to connect to an identity provider of your
   choice. Refer to the list of Dex [connectors](https://dexidp.io/docs/connectors/)
   for a list of supported providers.
2. Configure Antrea UI to connect to an OIDC-conformant provider. The provider
   could be an external instance of Dex managed by you, an OIDC service such as
   [Auth0](https://auth0.com/), etc.

In this document we will cover both methods.

## Method 1: Built-in Dex

When installing Antrea UI using Helm, you can choose to enable built-in support
for Dex, in which case an additional container named `dex` will be added to the
`antrea-ui` Deployment, and the OIDC client in the Antrea UI backend will be
configured automatically to access Dex.
[Connectors](https://dexidp.io/docs/connectors/) for the built-in Dex instance
can be configured through the `dex.config.connectors` Helm list value.

Let's assume that you want to deploy Antrea UI with built-in Dex support, and
with Github as the connector. You will first need to create a new
[Github OAuth app](https://docs.github.com/en/apps/oauth-apps/building-oauth-apps/creating-an-oauth-app),
with the following settings:

* Set the Application name to anything you want, e.g., `Antrea UI`.
* Set the Homepage URL to: `https://<ANTREA_UI_ADDRESS>`.
* Set the Authorization callback URL to: `https://<ANTREA_UI_ADDRESS>/dex/callback`.

After that, take note of the Client ID and generate a new Client Secret.

This is what the Antrea UI Helm configuration should look like:

```yaml
url: "https://<ANTREA_UI_ADDRESS>"
auth:
  basic:
    # if disabling password-based admin authentication is desired
    enable: false
  oidc:
    enable: true
dex:
  enable: true
  config:
    connectors:
    - type: github
      id: github
      name: Github
      config:
        clientID: "<GITHUB_APP_CLIENT_ID>"
        # secret provided by Github, *not* base64-encoded
        clientSecret: "<GITHUB_APP_CLIENT_SECRET>"
        redirectURI: "https://<ANTREA_UI_ADDRESS>/dex/callback"
security:
  cookieSecure: true
```

After installing Antrea UI with the correct configuration values, visit
`https://<ANTREA_UI_ADDRESS>` and click on the `Login With Dex` button. It will
redirect you to a Github login page, where you can enter your credentials.

Note that when logging out from the Antrea UI (by clicking the `Logout` button),
users will not be automatically logged out from the identity provider (Github in
the example above). This is not a feature supported by Dex.

## Method 2: OIDC-Conformant Provider

For this method, we will use [Auth0](https://auth0.com/) as an example, but
similar steps should work with other providers, as long as they are
OIDC-conformant.

In Auth0, register a new application of type "Regular Web Application". It is
important to note that while Antrea UI uses React as its frontend technology, we
implement a "Backend For Frontend" authentication pattern for simplicity and
security reasons. This enables us to use the OAuth2 "Authorization Code Flow",
as we can store an application secret securely in the backend. This is why the
registered Auth0 application should be of type "Regular Web Application", and
not "Single Page Application".

In the application settings, do the following:

* Take note of the Auth0 Domain, Client ID and Client Secret.
* Set the Application Login URI to: `https://<ANTREA_UI_ADDRESS>/auth/oauth2/login`.
* Add the following to the Allowed Callback URLs: `https://<ANTREA_UI_ADDRESS>/auth/oauth2/callback`.
* Add the following to the Allowed Logout URLs: `https://<ANTREA_UI_ADDRESS>`.

You can keep everything else as default. Configure the application Connections
(sources of users) as desired.

When installing Antrea UI, you can use a configuration like this one:

```yaml
url: "https://<ANTREA_UI_ADDRESS>"
auth:
  basic:
    # if disabling password-based admin authentication is desired
    enable: false
  oidc:
    enable: true
    providerName: "Auth0"
    # please note the trailing slash in the URL below
    issuerURL: "https://<AUTH0_DOMAIN>/"
    clientID: "<AUTH0_CLIENT_ID>"
    # the secret should be encoded in base64!
    # you can use: echo -n "<AUTH0_CLIENT_SECRET>" | base64
    clientSecret: "<AUTH0_CLIENT_SECRET>"
    # this enables logging out of Auth0 when the user logs out of Antrea UI
    logoutURL: "https://<AUTH0_DOMAIN>/v2/logout?returnTo={{LogoutReturnURL}}&client_id={{ClientID}}"
security:
  cookieSecure: true
```

When providing the `logoutURL` value, users will be automatically logged out of
Auth0 (the Auth0 SSO cookie will be invalidated). This configuration value
supports templating, and the following variables can be used: `{{Token}}` (the
ID token from the OIDC provider), `{{ClientID}}`, `{{URL}}` (which is set to
`https://<ANTREA_UI_ADDRESS>`), and `{{LogoutReturnURL}}` (which is the same as
`{{URL}}` with some relevant query parameters that will be consumed by the
Antrea UI frontend). If you do not provide `logoutURL`, users will remain signed
in to Auth0, even when they log out of Antrea UI, which means that they can
easily log back in without having to provide credentials.

After installing Antrea UI with the correct configuration values, visit
`https://<ANTREA_UI_ADDRESS>` and click on the `Login With Auth0` button.

## Using Kubernetes Secrets for OIDC Credentials

You can also provide OIDC client credentials from existing Kubernetes Secrets
instead of embedding them directly in the Helm values. This is particularly
useful when using external secret management solutions.

### Creating the Secret

First, create a Kubernetes Secret containing your OIDC credentials in the same
namespace where Antrea UI will be installed:

```bash
kubectl create secret generic oidc-credentials \
  --from-literal=clientID='<YOUR_CLIENT_ID>' \
  --from-literal=clientSecret='<YOUR_BASE64_ENCODED_CLIENT_SECRET>' \
  -n kube-system
```

Note: The client secret should be base64-encoded, just as it would be when
provided directly via Helm values.

### Using the Secret in Helm Configuration

You can reference the Secret in your Helm values configuration:

```yaml
url: "https://<ANTREA_UI_ADDRESS>"
auth:
  basic:
    enable: false
  oidc:
    enable: true
    providerName: "Auth0"
    issuerURL: "https://<AUTH0_DOMAIN>/"
    # Reference existing Secret instead of providing values directly
    clientIDSecretRef:
      name: "oidc-credentials"
      key: "clientID"
    clientSecretSecretRef:
      name: "oidc-credentials"
      key: "clientSecret"
    logoutURL: "https://<AUTH0_DOMAIN>/v2/logout?returnTo={{LogoutReturnURL}}&client_id={{ClientID}}"
security:
  cookieSecure: true
```

**Important:** When using Secret references, you cannot provide the `clientID`
or `clientSecret` values directly - they are mutually exclusive. The referenced
Secret(s) must exist in the same namespace as the Helm release before the
deployment can start successfully.
