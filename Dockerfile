# Build stage
FROM --platform=$BUILDPLATFORM golang AS builder
WORKDIR /app
COPY . .
RUN go mod download
RUN go build -o /Meower-Uploads

# Production stage
FROM golang:1.23-bookworm
COPY --from=builder /app/Meower-Uploads /Meower-Uploads
RUN apt-get update && apt-get install -y libc6
ENTRYPOINT ["/Meower-Uploads"]
