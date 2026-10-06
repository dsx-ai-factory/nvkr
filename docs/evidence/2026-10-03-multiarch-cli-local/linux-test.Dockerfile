FROM golang@sha256:9fef6c185fa8277ea98a09c2067351103b002cce3a421771dceb241c25a22529
RUN apt-get update && apt-get install -y --no-install-recommends jq && useradd --uid 1000 --create-home nkdtest
