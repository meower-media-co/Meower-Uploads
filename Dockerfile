# Build stage
FROM --platform=$BUILDPLATFORM golang AS builder
WORKDIR /app
COPY . .
RUN go mod download
RUN go build -o /Meower-Uploads

# Production stage
FROM debian
COPY --from=builder /app/Meower-Uploads /Meower-Uploads
RUN apt install libc6
ENTRYPOINT ["/Meower-Uploads"]
