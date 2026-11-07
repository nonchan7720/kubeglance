# Build with goreleaser (see .goreleaser.yaml): the binary is built outside
# and copied in, so the image is only the static binary on scratch.
FROM scratch
COPY kubeglance /kubeglance
USER 65534:65534
EXPOSE 8080
ENTRYPOINT ["/kubeglance"]
