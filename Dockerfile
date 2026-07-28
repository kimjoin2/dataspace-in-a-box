# The connector image exists to run the TCK harness. Native execution is the
# documented way to run dsbox; see DECISIONS.md section 17.
FROM golang:1.26 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /out/dsbox ./cmd/dsbox

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/dsbox /dsbox
EXPOSE 8080 8081
ENTRYPOINT ["/dsbox"]
CMD ["-config", "/etc/dsbox/config.yaml"]
