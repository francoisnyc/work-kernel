# The PoC apps: built with Node, served by nginx as a non-root user. The
# build context is apps/poc (docker-compose.yml), with two more read-only
# contexts: web/'s generated client (webgen) and its rank port (webrank),
# laid out where the app imports them from (../../web/src).
FROM node:24-alpine AS build
WORKDIR /repo/apps/poc
# Dependencies first, so a code change does not install them again.
COPY package.json package-lock.json ./
RUN --mount=type=cache,target=/root/.npm npm ci
COPY --from=webgen . /repo/web/src/gen
COPY --from=webrank . /repo/web/src/rank
COPY . .
# DEV ONLY: 1 compiles the sign-in list in. Only the compose stack sets
# it; a build without it has no sign-in list and no dev-user code.
ARG VITE_DEV_USERS=
ENV VITE_DEV_USERS=${VITE_DEV_USERS}
RUN npm run build

FROM nginxinc/nginx-unprivileged:1.29-alpine AS poc
COPY nginx.conf /etc/nginx/conf.d/default.conf
COPY --from=build /repo/apps/poc/dist /usr/share/nginx/html
EXPOSE 8080
