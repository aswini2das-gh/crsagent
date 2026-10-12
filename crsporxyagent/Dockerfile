# Build stage
FROM golang:1.22-alpine AS builder

WORKDIR /workspace

# Cache dependencies
COPY go.mod go.sum ./
RUN go mod download

# Copy source
COPY cmd/ cmd/
COPY api/ api/
COPY internal/ internal/

# Build
RUN CGO_ENABLED=0 GOOS=linux go build -a -o crsproxyagent cmd/main.go

# Runtime stage
FROM gcr.io/distroless/static:nonroot

WORKDIR /
COPY --from=builder /workspace/crsproxyagent .
USER 65532:65532

ENTRYPOINT ["/crsproxyagent"]
