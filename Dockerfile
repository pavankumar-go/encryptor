# Build the manager binary
FROM golang:1.19 as builder

WORKDIR /workspace
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o manager -mod=vendor cmd/main.go

# Refer to https://github.com/GoogleContainerTools/distroless for more details
FROM gcr.io/distroless/static:nonroot
WORKDIR /
COPY --from=builder /workspace/manager .
USER 65532:65532

ENTRYPOINT ["/manager"]
