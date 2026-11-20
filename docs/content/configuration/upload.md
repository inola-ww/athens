---
title: Uploading releases
description: Store releases of private modules that Athens can't fetch from a VCS
weight: 2
---

Athens normally fills its storage by fetching modules from a VCS or an upstream proxy. For a module that it can't fetch, for example a private module on a server that doesn't serve `go-import` meta tags, CI can upload each release instead.

## Configuration

```toml
UploadToken = "a long random value"
UploadPatterns = ["go.example.com/*"]
```

`UploadToken` (`ATHENS_UPLOAD_TOKEN`) enables the endpoint. `UploadPatterns` (`ATHENS_UPLOAD_PATTERNS`, comma separated) lists the modules that may be uploaded, with the glob rules of `GOPRIVATE`. Athens doesn't start when the token is set and the list is empty.

Global basic auth (`BASIC_AUTH_USER`) also uses the `Authorization` header, so uploads don't work together with it.

## Upload

```console
$ curl --fail-with-body -X PUT \
    -H "Authorization: Bearer $ATHENS_UPLOAD_TOKEN" \
    --data-binary @lib-v1.2.3.zip \
    "https://athens.example.com/go.example.com/lib/@v/v1.2.3.zip?time=2026-11-20T06:00:00Z"
{"Version":"v1.2.3","Time":"2026-11-20T06:00:00Z"}
```

The body is a module zip in the format of the go command. Make it with `golang.org/x/mod/zip` (`CreateFromDir` or `CreateFromVCS`). The module path in the URL uses the [case encoding](https://go.dev/ref/mod#goproxy-protocol) of the download protocol.

Athens checks the upload with the rules of the go command and refuses it when:

| Status | Reason |
| --- | --- |
| 401 | The bearer token is missing or wrong. |
| 403 | The module doesn't match `UploadPatterns`. |
| 400 | The version isn't a canonical release (pseudo-versions are refused), the major version doesn't match the module path, the zip is invalid or for another module@version, or it has no `go.mod` that declares the module. |
| 409 | The version is already in storage. Released versions are immutable; release a new version instead. |
| 413 | The zip is larger than 500 MiB. |

Athens takes the `.mod` file from the zip. It writes the `.info` file with the `time` query parameter (RFC 3339, optional, default: now). Use the commit time of the release so that `go list -m -json` shows the same time as for a VCS module.

## Serving uploaded modules

Uploaded modules exist only in storage. Set their [download mode](/configuration/download) to `none`, so that `/@v/list` and `/@latest` answer from storage and Athens doesn't try to fetch a missing version:

```hcl
downloadURL = "https://proxy.golang.org"
mode = "async_redirect"

download "go.example.com/*" {
    mode = "none"
}
```

The modules are not in the public checksum database. Clients set `GONOSUMDB=go.example.com` (or `GOPRIVATE` together with `GONOPROXY=none`), and Athens can enforce that with `NoSumPatterns`.
