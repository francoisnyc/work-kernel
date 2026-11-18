# Vendored proto

`google/rpc/error_details.proto` is an unchanged copy from
https://github.com/googleapis/googleapis (Apache 2.0). The item service puts a
`google.rpc.ErrorInfo` (the refusal's reason and metadata) and a
`google.rpc.BadRequest` (field violations) in every error; the web client
needs their schemas to read them. It is generated into `web/src/gen/` by
`make gen-web`. It is not part of the kernel's contract (`proto/kernel/`).
