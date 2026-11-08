# The end-to-end tests' helper (sleep, hog, receive): a static binary built
# outside (scripts/e2e.sh), on scratch like kubeglance itself.
FROM scratch
COPY helper /helper
USER 65534:65534
ENTRYPOINT ["/helper"]
