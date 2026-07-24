# Subscription Auth Guide

sage-wiki can authenticate using your existing LLM subscriptions --
ChatGPT Plus/Pro, Claude Pro/Max, GitHub Copilot, or Google Gemini --
instead of requiring separate API keys. If you already pay for one of
these subscriptions, you can use it to power sage-wiki without setting
up API billing.

## Why Use Subscription Auth

- **No separate API billing.** Use the subscription you already pay for.
- **No API keys to manage.** OAuth tokens are handled automatically.
- **Quick onboarding.** One command to log in or import credentials from
  tools you already use (Codex CLI, Claude Code, GitHub Copilot, Gemini CLI).

## Supported Providers

| Provider | Subscription Tiers | Login | Import |
|----------|-------------------|-------|--------|
| OpenAI | ChatGPT Plus, Pro | Yes | Yes |
| Anthropic | Claude Pro, Max | Yes | Yes |
| GitHub Copilot | Individual, Business, Enterprise | No | Yes |
| Google Gemini | Gemini Advanced | No | Yes |

**Login** opens a browser-based OAuth flow. **Import** reads tokens from
an existing CLI tool installed on your machine.

## Quick Start

The fastest path -- log in with your OpenAI account:

```bash
# Authenticate with your ChatGPT subscription
sage-wiki auth login --provider openai
```

Then set your config to use subscription auth:

```yaml
# config.yaml
api:
  provider: openai
  auth: subscription
```

Compile as usual:

```bash
sage-wiki compile
```

sage-wiki uses your subscription credentials instead of an API key.

## Login Flow

```bash
sage-wiki auth login --provider <name>
```

Where `<name>` is one of: `openai`, `anthropic`.

sage-wiki uses PKCE OAuth -- no client secret is stored, and tokens never
pass through a third-party server. It **always prints the authorization
URL**, attempts to open it in your default browser, and then waits for the
authorization to complete by **either** path below -- whichever happens
first (up to 5 minutes):

- **Local browser (desktop).** After you authorize, the browser redirects
  to a local callback (`http://localhost:<port>/callback`) that sage-wiki is
  listening on, and the tokens are stored automatically. The port is fixed
  per provider: **Anthropic `53692`**, **OpenAI `1455`** (it is shown in the
  `redirect_uri` of the printed URL).
- **Pasted redirect URL (headless/remote).** Copy the printed URL, open it
  in a browser on any machine, authorize, then paste the full redirect URL
  back into the terminal (see below).

### Headless Environments (SSH, WSL, Containers, VPS)

On a remote/headless box the browser runs on a *different* machine, so the
`http://localhost:<port>/callback` redirect lands on **your local machine**,
not the server -- the callback server on the remote box can never receive
it. Use the paste flow:

1. Run `sage-wiki auth login --provider <name>`. sage-wiki prints the
   authorization URL. (It also tries to launch a browser; on a headless box
   that does nothing useful, which is why the URL is always printed.)
2. Copy that URL and open it in a browser on any device.
3. Authorize. Your browser is redirected to
   `http://localhost:<port>/callback?code=...&state=...`. The page fails to
   load -- nothing is listening on *your* localhost -- which is expected.
4. Copy the **full redirect URL** from the browser's address bar (it still
   shows `?code=...&state=...` on the error page) and paste it at the prompt:

   ```
   paste it here (or just wait if the browser is on this machine):
   ```

5. sage-wiki exchanges the code and stores the tokens.

**Alternative -- SSH tunnel.** Because the callback port is fixed per
provider (Anthropic `53692`, OpenAI `1455`), you can forward it and let the
automatic flow complete instead of pasting:

```bash
# Anthropic (use 1455 for OpenAI)
ssh -L 53692:localhost:53692 user@your-server
# then run `sage-wiki auth login` on the server and authorize in your
# local browser -- the redirect reaches the server through the tunnel
```

On a server, **importing** credentials from an already-authenticated CLI
tool (see below) is often simpler than the login flow.

## Import from Existing CLI Tools

If you already use a CLI tool that has authenticated with your
subscription, sage-wiki can import those credentials directly:

```bash
sage-wiki auth import --provider <name>
```

### Where Tokens Are Read From

| Tool | Default Location | Override |
|------|-----------------|----------|
| Codex CLI | `~/.codex/auth.json` | `$CODEX_HOME` |
| Claude Code | `~/.claude/.credentials.json` | `$CLAUDE_CODE_OAUTH_TOKEN` env var |
| GitHub Copilot | `~/.copilot/settings.json` | `$COPILOT_HOME` |
| Gemini CLI | `~/.gemini/oauth_creds.json` | -- |

> **Claude Code (Anthropic).** sage-wiki reads Claude Code's
> `~/.claude/.credentials.json` directly, including its current format
> (OAuth tokens nested under `claudeAiOauth` with a millisecond `expiresAt`)
> as well as the older flat shape. Once imported, sage-wiki automatically
> sends the `anthropic-beta: oauth-2025-04-20` header that the Messages API
> requires for subscription tokens -- there is nothing extra to configure.

### macOS Note for Claude Code

On macOS, Claude Code stores credentials in the system Keychain rather
than a flat file. sage-wiki cannot read the Keychain directly. Export the
token via environment variable instead:

```bash
# Set the token from Claude Code's Keychain entry
export CLAUDE_CODE_OAUTH_TOKEN="your-token-here"
sage-wiki auth import --provider anthropic
```

When set, `CLAUDE_CODE_OAUTH_TOKEN` takes precedence over the credentials
file. The token is used as-is -- it has no refresh token, so sage-wiki cannot
auto-refresh it. When it expires you'll see a `401`; re-export a fresh token
and import again. (`auth status` shows it as `valid (no expiry)` since the
expiry isn't known.)

### Import Examples

```bash
# Import from Codex CLI
sage-wiki auth import --provider openai

# Import from Claude Code
sage-wiki auth import --provider anthropic

# Import from GitHub Copilot
sage-wiki auth import --provider github-copilot

# Import from Gemini CLI
sage-wiki auth import --provider gemini
```

## Configuration

Set `auth: subscription` under the `api` section in your `config.yaml`:

```yaml
api:
  provider: openai
  auth: subscription

models:
  summarize: gpt-4o-mini
  extract: gpt-4o-mini
  write: gpt-4o
  query: gpt-4o
```

An Anthropic example:

```yaml
api:
  provider: anthropic
  auth: subscription

models:
  summarize: claude-sonnet-4-20250514
  extract: claude-sonnet-4-20250514
  write: claude-sonnet-4-20250514
  query: claude-sonnet-4-20250514
```

## Auth Precedence

sage-wiki resolves credentials in this order:

1. **Environment variable** -- `OPENAI_API_KEY`, `ANTHROPIC_API_KEY`, etc.
   If set, used unconditionally regardless of config.
2. **Subscription auth** -- OAuth tokens from `auth login` or `auth import`.
   Used when `auth: subscription` is set in config and no env var overrides it.
3. **API key in config** -- The `api_key` field in `config.yaml`.
   Used when no env var is set and `auth` is not `subscription`.

This means you can set `auth: subscription` in your config for normal use
and still override with an API key via environment variable for CI or
scripted runs:

```bash
# CI: override subscription auth with an API key
OPENAI_API_KEY=sk-... sage-wiki compile
```

## Managing Credentials

### Check Auth Status

```bash
sage-wiki auth status
```

Shows the current provider, auth method, token expiry, and whether a
refresh token is available.

### Log Out

```bash
sage-wiki auth logout
```

Removes stored tokens for all providers. To log out from a specific
provider:

```bash
sage-wiki auth logout --provider openai
```

### Token Storage

Tokens are stored in `~/.sage-wiki/auth.json`. This file contains
OAuth access tokens and refresh tokens. It is created with `0600`
permissions (owner read/write only).

Do not commit this file to version control. sage-wiki's default
`.gitignore` excludes it.

## Multi-Provider Subscription Auth

You can authenticate with multiple providers and use different ones for
different tasks. A common setup is one provider for LLM generation and
another for embeddings:

```bash
# Authenticate with both providers
sage-wiki auth login --provider anthropic
sage-wiki auth import --provider gemini
```

```yaml
api:
  provider: anthropic
  auth: subscription

models:
  summarize: claude-haiku-4-5-20251001
  write: claude-sonnet-4-20250514
  query: claude-sonnet-4-20250514

embed:
  provider: gemini
  # Uses imported Gemini subscription credentials automatically
```

Note: The `embed` section currently uses its own `api_key` for
authentication, independent of the subscription auth system. If you use
subscription auth for your primary provider, you may still need an API key
or imported credentials for the embedding provider depending on your setup.

Check `sage-wiki auth status` to see which providers have stored credentials.

## Limitations

- **Batch mode unavailable.** Subscription tokens do not support batch
  API endpoints. `compile --batch` falls back to sequential requests.
- **Gemini prompt caching disabled.** Google's prompt caching requires
  API key auth. Subscription auth disables caching automatically.
- **Model restrictions.** Some models may not be available through
  subscription auth. If a model returns 403, check that your
  subscription tier includes access to it.
- **Rate limits differ.** Subscription rate limits are typically lower
  than API rate limits. sage-wiki's backoff and retry logic handles
  this, but compiles may take longer.
- **Terms of service.** Using subscription credentials outside the
  provider's official apps may violate their terms of service. Review
  your provider's acceptable use policy before relying on this in
  production.

## Troubleshooting

### Token Expired

```
Error: subscription token expired (openai)
```

Re-authenticate:

```bash
sage-wiki auth login --provider openai
```

If you imported credentials, re-import them -- the source tool may have
refreshed its own tokens:

```bash
sage-wiki auth import --provider openai
```

### Refresh Failed

```
Error: failed to refresh token (anthropic): invalid_grant
```

The refresh token has been revoked or expired. Log out and log in again:

```bash
sage-wiki auth logout --provider anthropic
sage-wiki auth login --provider anthropic
```

### 401 Unauthorized

The token is invalid or was revoked. Check `auth status` to see what
sage-wiki is sending:

```bash
sage-wiki auth status
```

If it shows a valid token, the provider may have revoked it (e.g.,
password change, subscription cancellation). Log in again.

### 403 Forbidden

The token is valid but your subscription does not include access to the
requested model. Either:

- Upgrade your subscription tier.
- Change the model in `config.yaml` to one your tier supports.

### Import Finds No Credentials

```
Error: no credentials found for openai at ~/.codex/auth.json
```

The source CLI tool may not be authenticated yet, or it stores
credentials in a non-default location. Check:

1. The source tool is installed and authenticated (`codex auth status`,
   `claude auth status`, etc.)
2. If using a custom home directory, set the override env var (e.g.,
   `$CODEX_HOME`)
3. On macOS with Claude Code, use `$CLAUDE_CODE_OAUTH_TOKEN` instead of
   file import

## Further Reading

- [Local Model Configuration](local-models.md) -- per-pass model routing with local and cloud models
- [Self-Hosted Server](self-hosted-server.md) -- Docker deployment, Syncthing, reverse proxy

## Credential storage backend (P2-6)

On systems with an OS keychain, credentials store via the OS keyring
(macOS Keychain, Windows Credential Manager, Secret Service) instead of
`~/.sage-wiki/auth.json`. Selection is automatic (read-only probe with a
500ms timeout — a locked or absent keyring means the file backend, which
is byte-identical to before). Run `sage-wiki auth migrate` once to move
existing file credentials; the file stays as a frozen point-in-time
backup and refreshed tokens never touch disk again. `auth status` shows
the active backend and where each credential lives. Note: if a provider
is ever removed from the supported set, its keychain entries can only be
cleared via the OS's own keychain UI (the app's closed provider set
can't enumerate them).
