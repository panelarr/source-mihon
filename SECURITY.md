# Security policy

## Reporting a vulnerability

Please report vulnerabilities **privately** through GitHub: open this repository's **Security** tab and choose **Report a vulnerability** (GitHub private vulnerability reporting). Don't open a public issue.

Include what you found, how to reproduce it and what impact you expect. We'll acknowledge the report, keep you updated and credit you in the advisory unless you'd rather stay anonymous.

## Supported versions

source-mihon is in early development. Only the latest commit on `main` gets fixes.

## Threat model in short

- **mihon-source runs untrusted extension code.** source-mihon only talks to it over its API, with a key; give that key only the scopes it needs.
- **Standalone plugins** listen on the network. Set an API key so only your host can call them.
- API keys and proxy credentials are never logged.
