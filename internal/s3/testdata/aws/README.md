# AWS S3 SigV4 fixtures

`signatures.json` transcribes the four request-signing examples published in
AWS's “Signature Calculations for the Authorization Header: Transferring
Payload in a Single Chunk” documentation:

https://docs.aws.amazon.com/AmazonS3/latest/developerguide/sig-v4-header-based-auth.html

AWS explicitly publishes these values as a test suite for custom SigV4
implementations. The list fixture intentionally reverses the raw query order to
prove Atomic canonicalizes it before signing; the expected signature is
unchanged.
