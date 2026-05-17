# web

> Fetch HTTP resources and post JSON bodies to remote endpoints.

## When to Use

- The user asks you to look up a URL or hit an API.
- You need to verify a remote endpoint is responding.

## Available Tools

- `http_get(url)` — GET a URL and return the response body as text.
- `http_post(url, body)` — POST a JSON body to a URL and return the response.

## Conventions

- Prefer HTTPS. Refuse to follow `http://` URLs unless the user explicitly OKs it.
- The response body returned to the model is truncated to 16 KiB — request narrower endpoints when possible.
- Never include API keys in the URL; use a separate `headers` argument (out of scope for this stub).

## Example

To fetch a JSON resource and report the title:

1. `http_get("https://example.org/feed.json")`.
2. Parse the body; reply with the relevant fields.
