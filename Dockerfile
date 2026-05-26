# Build stage
FROM golang:1.24-alpine AS builder

# Install build dependencies
RUN apk add --no-cache git openssh-client

WORKDIR /build

# Copy go mod files
COPY go.mod go.sum ./
RUN go mod download

# Copy all source packages
COPY main.go ./
COPY metrics/ ./metrics/
COPY nixproto/ ./nixproto/
COPY provisioner/ ./provisioner/

# Build the binary
RUN CGO_ENABLED=0 GOOS=linux go build -o nix-builder-provisioner .

# Runtime stage
FROM alpine:latest

# Install runtime dependencies
RUN apk add --no-cache \
    ca-certificates \
    openssh-client \
    wget \
    && rm -rf /var/cache/apk/*

# Install OpenTofu (Terraform alternative used in the project)
RUN wget -O /tmp/tofu.apk https://github.com/opentofu/opentofu/releases/download/v1.8.8/tofu_1.8.8_amd64.apk && \
    apk add --allow-untrusted /tmp/tofu.apk && \
    rm /tmp/tofu.apk

WORKDIR /app

# Copy binary from builder
COPY --from=builder /build/nix-builder-provisioner .

# Create necessary directories
# Note: terraform directory will be mounted as a volume at runtime
RUN mkdir -p /app/terraform/state && \
    mkdir -p /root/.ssh && \
    chmod 700 /root/.ssh

# Expose SSH proxy port
EXPOSE 2222

# Run the proxy
CMD ["/app/nix-builder-provisioner"]
