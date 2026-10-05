# Channel media storage

In split deployments, Server owns the concrete media storage provider:
`containerfs` with a host `localfs` fallback. Channel constructs its existing
`media.Service` with a `storageruntime.Provider` that proxies that same provider
over the authenticated Server RPC connection. Embedded deployments keep using
the Server's in-process provider.

The `StorageService` contract includes `Put`, `Open`, `Delete`, `AccessPath`,
`EnsureAccessPath`, `ListPrefix`, and `OpenContainerFile`. This preserves
storage-key lookup, current and legacy media directories, fallback storage,
and workspace file ingestion without sharing a filesystem with Channel.

Byte-bearing calls use 256 KiB chunks. Media ingestion and remote writes retain
the 200 MiB limit. The Server spools remote writes into a temporary file so the
fallback provider can rewind and retry a failed primary write. Channel cancels
the write stream when its source fails; closing a read stream cancels the remote
reader. RPC errors preserve registered storage sentinels and keep private
causes in the Server's result record.

## Upgrade order

Upgrade Server first, then Channel. Server registers both the Storage service
and its gRPC health status. Channel's `/ready` and `HEAD /health` require that
service to be serving, while `/ping` remains process liveness. A new Channel
connected to an older Server is unready instead of silently accepting a
configuration that cannot transfer attachments.

Server retains the media data volume, including any files previously written
by Channel into the shared volume. Channel no longer mounts that volume.
Workspace implementations stay behind the Server's provider; Cloud can retain
its provider-neutral workspace client and Runtime Worker/E2B routing.

## Verification

Run the Server and Channel composition tests, Storage RPC tests, and the
Channel architecture guard. The Storage RPC workspace test crosses the real
workspace bridge, concrete Server storage, authenticated RPC proxy,
`media.Service`, and outbound preparer, including an asset that exists only in
the legacy media directory. Runtime QA must also exercise an attachment flow
through the running application and inspect the resulting bytes and logs.
