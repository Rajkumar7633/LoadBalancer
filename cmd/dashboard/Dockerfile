FROM golang:1.24-alpine AS builder

# Install dependencies
RUN apk add --no-cache git curl

WORKDIR /app

# Copy go mod files
COPY go.mod go.sum ./
RUN go mod download

# Copy source code
COPY . .

# Build the dashboard application
RUN CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo -o dashboard ./cmd/dashboard

# Final stage
FROM alpine:latest

# Install curl for health checks
RUN apk --no-cache add curl

WORKDIR /app

# Copy the binary from builder stage
COPY --from=builder /app/dashboard .

# Copy web assets
COPY --from=builder /app/web ./web

# Create non-root user
RUN addgroup -g 1001 -S appgroup && \
    adduser -u 1001 -S appuser -G appgroup

# Change ownership of the app directory
RUN chown -R appuser:appgroup /app

# Switch to non-root user
USER appuser

# Expose port
EXPOSE 8081

# Health check
HEALTHCHECK --interval=30s --timeout=10s --start-period=40s --retries=3 \
    CMD curl -f http://localhost:8081/ || exit 1

# Run the application
CMD ["./dashboard"]
