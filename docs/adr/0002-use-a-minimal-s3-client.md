# ADR 0002: Use a minimal S3-compatible client

Status: Accepted, amended by
[ADR 0004](0004-use-a-remote-first-sqlite-catalog.md)

Date: 2026-07-11

## Decision

Ressik will use a small, standard-library S3 client that implements AWS
Signature Version 4 and only the object operations required by remote backup
destinations. AWS S3, Cloudflare R2, and Backblaze B2 will share this client by
supplying their endpoint and signing region.

The supported surface is PutObject, complete and single-range GetObject,
HeadObject, DeleteObject, and one page of ListObjectsV2. Range responses must
return the exact requested interval with HTTP 206 and a consistent
Content-Range; Ressik never falls back to downloading the complete object.
Requests use HTTPS, sign the SHA-256 payload, support temporary credential
session tokens, and never follow redirects. The client supports path-style and
virtual-hosted addressing but does not discover buckets or regions.

Ressik will not implement Signature Version 2, SigV4 streaming payloads,
multipart uploads, presigned URLs, bucket management, ACLs, tagging, or a
general AWS credential chain in this client. The S3 signer remains internal
because its path canonicalization rules are service-specific.

## Context

Ressik repositories contain immutable encrypted objects: blocks of about 4
MiB, manifests bounded near 64 MiB, and small commit markers. Remote
destinations copy those objects and do not need most of the S3 API. The three
initial object-storage providers expose the same five operations through an
S3-compatible API and require Signature Version 4 for private access.

An S3 access key is an input to request signing, not a bearer credential that
avoids signing. Temporary credentials add a session token but still require
SigV4. Presigned URLs are also SigV4 artifacts tied to a particular operation
and expiration, so they cannot provide durable unattended repository access.
Backblaze's native API avoids SigV4 but would require a second object protocol
and separate retry behavior.

Automatic remote delivery is intentionally outside this decision. The local
backup engine currently commits and applies retention before returning. Remote
delivery needs durable reconciliation so a transient network failure cannot
leave a committed snapshot permanently absent from one destination.

## Consequences

All three providers can share one client without adding an AWS SDK or any new
module dependency. Replayable, bounded byte payloads make request retries and
payload signing straightforward, and official AWS signature examples can
freeze canonicalization behavior.

Ressik now owns sensitive authentication code and must maintain golden SigV4
tests, endpoint compatibility tests, and bounded XML parsing. Once provider
adapters land, their release gate must also include opt-in integration tests
against each service. Features outside the deliberately small operation set
need an explicit decision instead of accumulating SDK-shaped surface area.

AWS-specific credential discovery, role refresh, multi-region access points,
and SigV4A are not available. A later credential source may still supply
temporary access key, secret, and session-token values to this client.

## Alternatives considered

- The AWS SDK handles more endpoint and credential variants, but its API and
  dependency surface substantially exceed Ressik's five fixed operations.
- Presigned URLs avoid signing at the point of use but still require a signer,
  expire, and authorize only selected operations on selected objects.
- A Backblaze-native adapter avoids SigV4 for B2 only and duplicates the common
  upload, listing, error, and retry path.

## References

- [AWS S3 Signature Version 4](https://docs.aws.amazon.com/AmazonS3/latest/developerguide/sig-v4-authenticating-requests.html)
- [Cloudflare R2 S3 compatibility](https://developers.cloudflare.com/r2/api/s3/api/)
- [Backblaze B2 S3-compatible API](https://www.backblaze.com/docs/en/cloud-storage-call-the-s3-compatible-api)
