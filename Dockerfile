# Build stage
FROM golang AS build-stage
WORKDIR /app
COPY . .
RUN go mod download
RUN go build -o /Meower-Uploads

# Production stage
FROM debian:bookworm-slim AS production-stage
COPY --from=build-stage /Meower-Uploads /Meower-Uploads
EXPOSE 3001
ENTRYPOINT ["/Meower-Uploads"]