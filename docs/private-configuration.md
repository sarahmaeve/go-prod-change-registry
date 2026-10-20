# Private deployment configuration

PCR's source and example manifests are deployment-neutral. The `pcr` CLI
requires an explicit installation URL; missing configuration must never
select a production service implicitly.

## What stays private

Keep real service origins, database addresses, identity-provider settings,
access groups, integration identifiers, and network selectors in a separate
private deployment repository. Inject credentials through its secret manager.
Do not put live tokens, app passwords, or exported event data in example files,
test fixtures, screenshots, or public build artifacts.

For workstation use, prefer configuration outside the checkout. PCR creates
its platform-default TOML file with mode `0600`; use `pcr config path` to find
it. In PCR's checkout, local `.env`/`.env.*` files (except the Git-allowed
`.env.example` template) and `private/` are excluded from Git and the Docker
build context as a convenience, not a security boundary. Ignoring
files does not protect files already tracked, copied elsewhere, or present in
Git history. PCR's Go configuration loader does not automatically read these
files; provide values through the process environment or PCR's
explicit config-file option.

## PCR CLI

Replace the example origin with the installation's real URL locally:

```bash
pcr --url https://changes.example.com config init
pcr config set-credential
pcr config show
```

URL precedence is `--url`, `PCR_URL`, then the TOML `url` field. Automation
should receive `PCR_URL` from private configuration and `PCR_CREDENTIAL` from
the secret manager. There is no compiled fallback. Existing TOML files with
an explicit URL continue to work; installations that previously relied on the
implicit destination must set it before upgrading the CLI.

## Kubernetes and publication

The `k8s/` files are templates, not your production configuration. Copy or
overlay them in your private deployment repository; keep secret values in its
secret manager. Adapt the trusted-proxy NetworkPolicy selectors to the actual
workloads and retain the proxy-only network boundary. See
[deployment instructions](deployment.md).

Before publishing, review the exact staged files and any artifacts, screenshots,
and Git history being published for private data. Cleaning the current source
tree does not erase previously committed values. No history rewrite or live
deployment is part of this configuration separation.
