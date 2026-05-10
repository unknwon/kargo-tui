# Vendored Kargo API stubs

This directory contains a patched copy of the Kargo API's generated protobuf
and Connect-RPC client stubs.

## Why this exists

The upstream `github.com/akuity/kargo/api/service/v1alpha1/svcv1alpha1connect`
package **panics at init** when imported into a Go program:

```
panic: message *v1.ConfigMap is neither a v1 or v2 Message
```

The Kargo API embeds Kubernetes `corev1.ConfigMap` and `corev1.Secret` in
its proto messages (for the project-level config-map / repo-credential
RPCs). Those Kubernetes types are generated with **gogo-proto v1**, which
is incompatible with the **v2 protobuf reflection** that Kargo's generated
Connect stub uses for lazy descriptor resolution. When the v2 resolver
walks the service file's descriptor graph and reaches the gogo-typed
field, it panics — there is no v2 reflect interface on the gogo Go type.

Because all messages in `service/v1alpha1/service.pb.go` share one
descriptor file, the panic also fires the moment any other message in that
file is marshalled — so importing only the `pb` types and skipping the
connect stub doesn't help either.

## Why we have to vendor instead of patching at runtime

The v2 protobuf resolver inspects Go type pointers (`*v1.ConfigMap`)
directly, not the global type registry by name. Registering a synthetic
descriptor under the right name does nothing — the resolver never asks the
registry. The only fix is to make the Go type referenced by the field
satisfy the v2 `protoreflect.ProtoMessage` interface, and we can't add
methods to a foreign-package type. So vendoring + import rewriting is the
minimum-pain path.

## Why we accept JSON-without-real-timestamps over vendoring

Vendoring drags in tens of thousands of lines of generated code we don't
maintain. Every Kargo API release means re-vendoring + re-patching. Once
upstream completes the gogo→v2 migration (in flight across the K8s
ecosystem), the whole apparatus becomes dead weight.

If you're reading this because we *did* eventually vendor: the trigger
was that ULID/annotation-derived timestamp fallbacks weren't enough for a
specific user-visible problem. Document the problem here so we know what
we'd lose by reverting.

## Layout (when vendored)

- `corev1stub/` — minimal v2-compliant `ConfigMap` and `Secret` so the
  rewritten import path resolves to types the v2 reflector accepts. We
  never call ConfigMap/Secret RPCs, so empty message bodies are sufficient.
- `svc/` — copy of `service/v1alpha1/service.pb.go` with the
  `k8s.io/api/core/v1` import rewritten to `corev1stub`.
- `svcconnect/` — copy of `service/v1alpha1/svcv1alpha1connect/` with the
  service import rewritten to point at our vendored `svc/`.
- `v1alpha1/` — copy of `api/v1alpha1/generated.pb.go` (referenced by
  `service.pb.go` for Stage/Freight/Promotion message types) with the same
  rewrites.
- `rbac/` — copy of `api/rbac/v1alpha1/generated.pb.go` (referenced by
  service stubs for Role/RoleBinding RPCs) with the same rewrites.

## Re-vendoring on a Kargo bump

1. `go get github.com/akuity/kargo/api@<new-version>`
2. Re-run the vendor script in `scripts/vendor-kargoapi.sh` (it copies the
   four files above and applies the import rewrites).
3. Run `go build ./...`, fix any new corev1 usages by extending
   `corev1stub` with whatever new gogo type the upstream introduced.
