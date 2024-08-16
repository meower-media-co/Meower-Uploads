FROM --platform=$BUILDPLATFORM golang
WORKDIR /app
COPY . .
RUN go mod download
RUN go build -o /Meower-Uploads
ENTRYPOINT ["sh", "-c", "update-ca-certificates && exec /Meower-Uploads"]
