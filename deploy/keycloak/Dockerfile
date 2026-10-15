# Grantra Login: Keycloak with the Grantra theme built in. The Helm chart runs
# this image; docker-compose mounts the theme into the stock image instead, so
# theme edits show up without a rebuild.
FROM quay.io/keycloak/keycloak:26.4
COPY --chown=1000:0 themes/grantra /opt/keycloak/themes/grantra
